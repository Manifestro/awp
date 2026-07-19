import asyncio
import os
import secrets
import uuid
from collections import defaultdict
from datetime import datetime, timezone
from typing import Any, Literal

from fastapi import FastAPI, Header, HTTPException, WebSocket, WebSocketDisconnect, status
from pydantic import BaseModel, ConfigDict, Field, ValidationError, field_validator


PROTOCOL_VERSION = "0.1"
SERVER_VERSION = "0.1.0"
MAX_MESSAGE_BYTES = 64 * 1024
HEARTBEAT_INTERVAL_SECONDS = 30
AWP_TOKEN = os.getenv("AWP_TOKEN", "local-dev-token")


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def new_id(prefix: str) -> str:
    return f"{prefix}_{uuid.uuid4().hex}"


def validate_rfc3339(value: str) -> str:
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise ValueError("timestamp must be RFC 3339") from error
    if parsed.tzinfo is None:
        raise ValueError("timestamp must include a timezone")
    return value


class Envelope(BaseModel):
    model_config = ConfigDict(extra="ignore")

    type: Literal["awp"] = "awp"
    version: Literal["0.1"] = PROTOCOL_VERSION
    id: str = Field(min_length=1)
    action: str = Field(min_length=1)
    timestamp: str = Field(min_length=1)
    data: dict[str, Any]

    @field_validator("timestamp")
    @classmethod
    def timestamp_must_be_rfc3339(cls, value: str) -> str:
        return validate_rfc3339(value)


class ClientInfo(BaseModel):
    name: str
    version: str


class Capabilities(BaseModel):
    adapters: list[str] = Field(default_factory=list)
    resume: bool = False


class ClientHelloData(BaseModel):
    device_id: str = Field(min_length=1)
    client: ClientInfo
    capabilities: Capabilities


class SessionBindData(BaseModel):
    session_id: str = Field(min_length=1)
    adapter: str = Field(min_length=1)
    metadata: dict[str, Any] = Field(default_factory=dict)


class Target(BaseModel):
    device_id: str = Field(min_length=1)
    session_id: str = Field(min_length=1)


class Event(BaseModel):
    source: str = Field(min_length=1)
    name: str = Field(min_length=1)
    timestamp: str = Field(min_length=1)
    data: dict[str, Any]

    @field_validator("timestamp")
    @classmethod
    def timestamp_must_be_rfc3339(cls, value: str) -> str:
        return validate_rfc3339(value)


class EventPublishData(BaseModel):
    event_id: str = Field(min_length=1)
    target: Target
    event: Event


class EventAckData(BaseModel):
    delivery_id: str = Field(min_length=1)
    event_id: str = Field(min_length=1)
    status: Literal["accepted", "completed", "failed", "rejected"]
    result: dict[str, Any] | None = None


class PublishResult(BaseModel):
    event_id: str
    delivery_id: str
    status: str
    online: bool
    duplicate: bool = False


class SessionBinding(BaseModel):
    session_id: str
    device_id: str
    adapter: str
    metadata: dict[str, Any]


class DeliveryRecord(BaseModel):
    delivery_id: str
    event_id: str
    device_id: str
    session_id: str
    message: dict[str, Any]
    status: str = "pending"


def envelope(action: str, data: dict[str, Any], *, message_id: str | None = None) -> dict[str, Any]:
    return {
        "type": "awp",
        "version": PROTOCOL_VERSION,
        "id": message_id or new_id("msg"),
        "action": action,
        "timestamp": utc_now(),
        "data": data,
    }


def authorized(value: str | None) -> bool:
    if not value or not value.startswith("Bearer "):
        return False
    return secrets.compare_digest(value.removeprefix("Bearer "), AWP_TOKEN)


def require_http_auth(authorization: str | None) -> None:
    if not authorized(authorization):
        raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="Invalid AWP bearer token")


class AWPState:
    def __init__(self) -> None:
        self.connections: dict[str, WebSocket] = {}
        self.sessions: dict[str, SessionBinding] = {}
        self.deliveries: dict[str, DeliveryRecord] = {}
        self.event_deliveries: dict[str, str] = {}
        self.pending: dict[str, list[str]] = defaultdict(list)
        self.lock = asyncio.Lock()

    async def connect(self, device_id: str, websocket: WebSocket) -> WebSocket | None:
        async with self.lock:
            previous = self.connections.get(device_id)
            self.connections[device_id] = websocket
            return previous

    async def disconnect(self, device_id: str, websocket: WebSocket) -> None:
        async with self.lock:
            if self.connections.get(device_id) is websocket:
                self.connections.pop(device_id, None)

    async def bind(self, device_id: str, binding: SessionBindData) -> SessionBinding:
        value = SessionBinding(
            session_id=binding.session_id,
            device_id=device_id,
            adapter=binding.adapter,
            metadata=binding.metadata,
        )
        async with self.lock:
            existing = self.sessions.get(binding.session_id)
            if existing and existing.device_id != device_id:
                raise ValueError("session is already bound to another device")
            self.sessions[binding.session_id] = value
        return value

    async def publish(self, published: EventPublishData) -> tuple[DeliveryRecord, bool, bool]:
        async with self.lock:
            existing_delivery_id = self.event_deliveries.get(published.event_id)
            if existing_delivery_id:
                record = self.deliveries[existing_delivery_id]
                return record, record.device_id in self.connections, True

            binding = self.sessions.get(published.target.session_id)
            if binding is None:
                raise KeyError("session_not_found")
            if binding.device_id != published.target.device_id:
                raise ValueError("session_device_mismatch")

            delivery_id = new_id("dlv")
            message = envelope(
                "event.deliver",
                {
                    "delivery_id": delivery_id,
                    "event_id": published.event_id,
                    "target": published.target.model_dump(),
                    "event": published.event.model_dump(),
                    "attempt": 1,
                },
            )
            record = DeliveryRecord(
                delivery_id=delivery_id,
                event_id=published.event_id,
                device_id=published.target.device_id,
                session_id=published.target.session_id,
                message=message,
            )
            self.deliveries[delivery_id] = record
            self.event_deliveries[published.event_id] = delivery_id
            self.pending[published.target.device_id].append(delivery_id)
            online = published.target.device_id in self.connections
            return record, online, False

    async def acknowledge(self, device_id: str, acknowledged: EventAckData) -> None:
        async with self.lock:
            record = self.deliveries.get(acknowledged.delivery_id)
            if record is None or record.event_id != acknowledged.event_id:
                raise KeyError("delivery_not_found")
            if record.device_id != device_id:
                raise PermissionError("delivery belongs to another device")
            record.status = acknowledged.status
            if acknowledged.status in {"accepted", "completed", "failed", "rejected"}:
                self.pending[device_id] = [
                    item for item in self.pending[device_id] if item != acknowledged.delivery_id
                ]

    async def send(self, device_id: str, payload: dict[str, Any]) -> bool:
        async with self.lock:
            websocket = self.connections.get(device_id)
        if websocket is None:
            return False
        try:
            await websocket.send_json(payload)
            return True
        except Exception:
            await self.disconnect(device_id, websocket)
            return False

    async def deliver_pending(self, device_id: str) -> None:
        async with self.lock:
            delivery_ids = list(self.pending.get(device_id, []))
            messages = [self.deliveries[item].message for item in delivery_ids if item in self.deliveries]
        for message in messages:
            if not await self.send(device_id, message):
                return

    async def counts(self) -> dict[str, int]:
        async with self.lock:
            return {
                "connections": len(self.connections),
                "sessions": len(self.sessions),
                "deliveries": len(self.deliveries),
                "pending": sum(len(items) for items in self.pending.values()),
            }


state = AWPState()
app = FastAPI(title="AWP Example Service", version=SERVER_VERSION)


async def send_error(
    websocket: WebSocket,
    *,
    code: str,
    message: str,
    reply_to: str | None = None,
    retryable: bool = False,
) -> None:
    await websocket.send_json(
        envelope(
            "error",
            {
                "code": code,
                "message": message,
                "reply_to": reply_to,
                "retryable": retryable,
            },
        )
    )


@app.get("/health")
async def health() -> dict[str, Any]:
    return {"status": "ok", "protocol_version": PROTOCOL_VERSION, **await state.counts()}


@app.websocket("/awp")
async def websocket_endpoint(websocket: WebSocket) -> None:
    if not authorized(websocket.headers.get("authorization")):
        await websocket.close(code=1008, reason="Invalid AWP bearer token")
        return

    await websocket.accept()
    device_id: str | None = None
    try:
        try:
            raw_hello = await asyncio.wait_for(websocket.receive_json(), timeout=10)
            hello = Envelope.model_validate(raw_hello)
            if hello.action != "client.hello":
                await send_error(
                    websocket,
                    code="hello_required",
                    message="The first message must be client.hello",
                    reply_to=hello.id,
                )
                await websocket.close(code=1002)
                return
            hello_data = ClientHelloData.model_validate(hello.data)
        except (ValidationError, ValueError, asyncio.TimeoutError) as error:
            await send_error(websocket, code="invalid_hello", message=str(error))
            await websocket.close(code=1002)
            return

        device_id = hello_data.device_id
        previous = await state.connect(device_id, websocket)
        if previous and previous is not websocket:
            await previous.close(code=1012, reason="A newer connection replaced this device")

        await websocket.send_json(
            envelope(
                "server.welcome",
                {
                    "device_id": device_id,
                    "connection_id": new_id("conn"),
                    "heartbeat_interval_seconds": HEARTBEAT_INTERVAL_SECONDS,
                    "max_message_bytes": MAX_MESSAGE_BYTES,
                },
            )
        )
        await state.deliver_pending(device_id)

        while True:
            try:
                message = Envelope.model_validate(await websocket.receive_json())
            except ValidationError as error:
                await send_error(websocket, code="invalid_message", message=str(error))
                continue

            try:
                if message.action == "session.bind":
                    binding = await state.bind(device_id, SessionBindData.model_validate(message.data))
                    await websocket.send_json(
                        envelope(
                            "session.bound",
                            {"session_id": binding.session_id, "status": "active"},
                        )
                    )
                elif message.action == "event.ack":
                    await state.acknowledge(device_id, EventAckData.model_validate(message.data))
                elif message.action == "heartbeat.ping":
                    await websocket.send_json(envelope("heartbeat.pong", {"reply_to": message.id}))
                elif message.action == "heartbeat.pong":
                    continue
                else:
                    await send_error(
                        websocket,
                        code="unexpected_action",
                        message=f"Client action '{message.action}' is not allowed",
                        reply_to=message.id,
                    )
            except ValidationError as error:
                await send_error(
                    websocket,
                    code="invalid_action_data",
                    message=str(error),
                    reply_to=message.id,
                )
            except (KeyError, PermissionError, ValueError) as error:
                await send_error(
                    websocket,
                    code=str(error).strip("'"),
                    message=str(error),
                    reply_to=message.id,
                )
    except WebSocketDisconnect:
        pass
    finally:
        if device_id:
            await state.disconnect(device_id, websocket)


@app.post("/events", response_model=PublishResult, status_code=status.HTTP_202_ACCEPTED)
async def publish_event(
    message: Envelope,
    authorization: str | None = Header(default=None),
) -> PublishResult:
    require_http_auth(authorization)
    if message.action != "event.publish":
        raise HTTPException(status_code=400, detail="Action must be event.publish")
    try:
        published = EventPublishData.model_validate(message.data)
        delivery, online, duplicate = await state.publish(published)
    except ValidationError as error:
        raise HTTPException(status_code=422, detail=error.errors()) from error
    except KeyError as error:
        raise HTTPException(status_code=404, detail=str(error).strip("'")) from error
    except ValueError as error:
        raise HTTPException(status_code=409, detail=str(error)) from error

    if online and not duplicate:
        online = await state.send(delivery.device_id, delivery.message)

    return PublishResult(
        event_id=delivery.event_id,
        delivery_id=delivery.delivery_id,
        status=delivery.status,
        online=online,
        duplicate=duplicate,
    )
