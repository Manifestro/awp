# Agent Wake Protocol (AWP)

Статус: ранний черновик  
Версия wire protocol: `0.1`

## 1. Назначение

Agent Wake Protocol — стандарт обратной доставки событий от внешнего сервиса в неактивную локальную агентную сессию.

MCP и AWP дополняют друг друга:

```text
Agent ── MCP ──▶ Provider: вызвать инструмент сейчас
Agent ◀─ AWP ─── Provider: получить событие позже
```

Например, Sinores предоставляет оба endpoint:

```text
MCP: https://sinores.net/mcp
AWP: wss://sinores.net/awp
```

AWP не является отдельным центральным сервером. Каждый продукт самостоятельно реализует и обслуживает собственный AWP endpoint рядом со своим MCP или API.

## 2. Общая модель

```text
Sinores MCP/AWP ─────┐
GitHub MCP/AWP ──────┼──▶ Local AWP daemon ──▶ Codex/Claude Code sessions
Email MCP/AWP ───────┘
```

Локальный daemon устанавливает исходящее WebSocket-соединение с каждым настроенным provider. Поэтому компьютеру пользователя не нужны публичный домен, статический IP, открытый порт или port forwarding.

Соединения providers независимы. Недоступность одного продукта не должна останавливать события остальных.

## 3. Роли

### 3.1. AWP Provider

AWP Provider — продукт, который уже предоставляет агенту MCP или API и хочет отправлять события в обратном направлении.

Примеры:

- Sinores отправляет `message.received`;
- GitHub-интеграция отправляет `pull_request.comment.created`;
- почтовый provider отправляет `email.received`;
- monitoring provider отправляет `alert.created`.

Provider обязан:

- предоставить аутентифицированный `wss://.../awp` endpoint;
- принимать `client.hello` и `session.bind`;
- хранить собственные недоставленные события;
- доставлять их правильному `device_id` и `session_id`;
- обрабатывать ACK, retry, deduplication и heartbeat;
- связать свои прикладные ресурсы с opaque AWP session ID.

Provider не знает внутренний Codex/Claude runtime session ID.

### 3.2. AWP Client/Daemon

AWP Client работает локально рядом с agent runtime.

Он обязан:

- поддерживать отдельное исходящее соединение с каждым provider;
- зарегистрировать несколько локальных сессий на одном provider connection;
- маршрутизировать события по `(provider, session_id)`;
- возобновить правильную runtime session;
- передать событие как недоверенные внешние данные;
- отправить результат обработки обратно тому же provider.

### 3.3. Agent Runtime

Codex, Claude Code или другой runtime хранит и возобновляет агентную сессию. Runtime adapter является локальной частью AWP Client и сохраняет исходные права сессии.

## 4. Пример Sinores

```text
1. Codex через https://sinores.net/mcp отправляет WhatsApp-сообщение.
2. Codex завершает текущий turn.
3. Sinores получает новый ответ WhatsApp.
4. wss://sinores.net/awp доставляет event.deliver.
5. Local AWP daemon находит binding (sinores, ses_support).
6. Codex adapter возобновляет соответствующую локальную сессию.
7. Daemon отправляет completed или failed в Sinores AWP.
```

Прикладное событие может выглядеть так:

```json
{
  "source": "sinores",
  "name": "message.received",
  "timestamp": "2026-07-19T12:00:02Z",
  "data": {
    "channel_id": "channel_123",
    "from": "+77001234567",
    "message_id": "wa_message_456",
    "text": "Да, давай завтра в 10"
  }
}
```

## 5. Provider-scoped bindings

`session_id` является opaque идентификатором внутри provider. Локальный уникальный ключ:

```text
(provider, session_id)
```

Пример локального registry:

```text
sinores / ses_support → codex / runtime_A
sinores / ses_sales   → codex / runtime_B
github  / ses_project → codex / runtime_C
```

Одинаковый `session_id` может существовать у разных providers без конфликта.

Как конкретный ресурс provider связывается с AWP session, зависит от продукта до появления общего стандарта. Например, MCP-вызов может принять `awp_session_id`, либо provider может предоставить отдельный MCP tool для association/subscription.

## 6. Основные принципы

- каждый MCP/API provider владеет собственным AWP endpoint;
- центральный AWP relay не требуется;
- один daemon подключается к нескольким providers;
- один provider connection обслуживает несколько сессий;
- runtime identity и permissions остаются локальными;
- события доставляются at least once;
- payload является opaque и недоверенным JSON;
- отказ одного provider изолирован от остальных.

## 7. Что стандартизирует AWP

- JSON envelope и action schemas;
- client hello и capability advertisement;
- provider-scoped session binding;
- event delivery и acknowledgement states;
- heartbeat;
- retry и deduplication semantics;
- error messages;
- требования к runtime adapters.

AWP не стандартизирует внутреннюю бизнес-схему `event.data` и не заменяет MCP.

## 8. Открытые вопросы

- общий MCP method/tool для association ресурса с AWP session;
- pairing и выдача provider credentials;
- subscription/filter schema;
- token rotation и revocation;
- batching и wake coalescing;
- manual approval policies;
- JSON Schema и conformance suite;
- negotiation следующих protocol versions.

## 9. Главный принцип

> MCP — исходящий канал активного агента к provider. AWP — принадлежащий этому provider обратный событийный канал к неактивной агентной сессии.
