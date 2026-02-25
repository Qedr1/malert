# Алертинг
### Стек 
- Go (1.23.1+)
- Опционально NATS Server JetStream (2.12.4+)
- Опционально ClickHouse (25.11+)

## Сценарии использования
После этапа агрегации входящих событий согласно типу алертов, сервис принимает решение 
- поднять алерт
- маршрутизировать алерт
- если события по алерту больше не поступают, алерт гасится согласно заданым порогам

### Основной
Агрегаты метрик формируются в хранилище метрик - ClickHouse и через mat.view c nats  engine передаются в nats. Алертинг по подписке принимает все агрегаты и по своим правилам взводит или гасит алерт. 

Преимущества решения
  1. на порядки снижается трафик  в алертинг
  1. алертинг работает в реальном времени вне зависимости от количества данных. агрегаты формируются и передаются в nats в момент вставки новых данных в хранилище

### Произвольный 
Предполагает формирование агрегатов на стороне самих сервисов и доставку их в алертинг через nats или HTTP

## Режимы работы (single vs multi)
#### single-instance
Может быть запущена одна копия сервиса c единственных входящим каналом событий по HTTP
#### multi-instance 
Произвольное нечетное количество сервисов с межсервисной синхронизацией и дополнительным входящим каналом через NATS
| Характеристика | single-instance | multi-instance |
|---|---|---|
| Выбор режима | service.mode=single | service.mode=nats (default) |
| Входящие события | только HTTP (ingest.http) | HTTP + NATS (ingest.nats) |
| Хранение состояния | in-memory (локально) | NATS JetStream KV (tick, data) |
| Синхронизация между инстансами | нет | через NATS (KV + delete-marker consumer) |
| Зависимость от NATS | отсутствует | обязательна |
| Notify queue | отключена (не используется) | доступна через JetStream (notify.queue) |
| Масштабирование | вертикальное | горизонтальное (несколько инстансов) |
| Доступность | зависит от одного процесса | распределенная (при живом NATS) |
| Перезапуск сервиса | состояние теряется | состояние сохраняется в NATS |

Ограничения single-instance:
- вход только HTTP;
- нет ingest через NATS;
- нет notify queue;
- состояние только в памяти процесса.

### Входящие интерфейсы
- single-instance: встроенный http_server.
- multi-instance: встроенный http_server + подписка на nats+синхронизация сервисов через nats.

### Исходящие интерфейсы
- http_client: jira, youtrack, [ произвольный контракт ]
- telegram
- mattermost

## Целевая рабочая нагрузка и доступность
- raw нагрузка multi-instance: 1 000 000 events/sec.
- ingest нагрузка multi-instance: 200 000 events/sec.
- доступность multi-instance: 99.99%

##  Фактическая  нагрузка
### Стенд
- vCPU: 1
- RAM: 256
### HTTP 
Входящих (ingest) событий/сек
| Тип метрики | batch=1  | batch=100 | batch=1000 |
|---|---:|---:|---:|
| count_total | 13 408 | 266 696 | 383 322 |
| count_window | 13 504 | 258 132 | 378 275 |
| missing_heartbeat | 13 376 | 260 619 | 380 405 |

### NATS 
Входящих (ingest) событий/сек
| Тип метрики | batch=1 | batch=100 | batch=1000 |
|---|---:|---:|---:|
| count_total | 2 587 | 384 826 | 422 513 |
| count_window | 2 447 | 382 404 | 413 736 |
| missing_heartbeat | 2 583 | 388 332 | 416 613 |


# Принцип работы
```    
                   [ jira ] [ youtrack ] [ muttermost ] [ telegram ]
                       |__________|_____________|___________|
                                          ▲
                                    ______|_______
                                    ▲            ▲
                                    |            |
                                mALERT_1 … mALERT_N 
                                    ▲            ▲
                                    |            |
[ vector ] ──► [ ClickHouse ] ──► [ nats ]     http_server 
    ▲                ▲              ▲            ▲
    |                |              |            |
[ mAGENT ]      [ serv1 ]        [ servN ]     servN+1

mAGENT https://github.com/Qedr1/magent
 ```
1. Событие приходит через HTTP (ingest.http) или NATS (ingest.nats).
2. Событие валидируется по контракту (dt, type, tags, var, value, agg_cnt, win).
3. Для каждого правила выполняются:
- фильтр match (type/var/tags/value),
- проверка out-of-order (max_late_ms, max_future_skew_ms),
- вычисление alert_id.

4. alert_id формируется детерминированно:
rule/<sanitized_rule>/<sanitized_var>/<sha1(key.from_tags)>.
Одинаковые key.from_tags + одинаковые значения тегов дают один и тот же alert_id.

5. Движок обновляет runtime-состояние алерта по трем типам алертов:
- count_total: накапливает счетчик,
- count_window: считает скользящее окно,
- missing_heartbeat: срабатывает по отсутствию событий после первого heartbeat.

6. Переходы состояний:
pending -> firing -> resolved.
pending опционален (pending.enabled, pending.delay_sec).

7. Состояние:
- multi-instance: NATS KV:
  - tick — TTL-ключ активности алерта,
  - data — карточка алерта (rule/var/tags/state/timestamps/external refs).
- single-instance: in-memory (без NATS KV).

8. Закрытие алерта:
- для count_total и count_window: по resolve.silence_sec + resolve.hysteresis_sec,
- для missing_heartbeat: по raise.missing_sec и подтвержденному возврату heartbeat (resolve.hysteresis_sec),
- также по факту TTL-удаления tick (delete marker).

9. Уведомления:
- либо direct-режим (notify.queue.enabled=false),
- либо queue-режим через JetStream worker (notify.queue.enabled=true).
Маршрутизация задается в [[rule.<rule_name>.notify.route]] (channel, template).

10. Повторы firing управляются notify.repeat*.
В queue-режиме доставка per-channel best-effort (ошибка одного канала не блокирует другие).


## Входящие события
Структура
```
{
  "dt": 1739876543210,                       // unixtime ms: время события или конец окна агрегации
  "type": "event",                           // "event" | "agg"
  "tags": { "dc": "dc1", "project": "p1" },  // теги 
  "var": "rx_bytes",                         // имя переменной/метрики/сигнала
  "value": { "t": "n", "n": 123 },           // значение ВСЕГДА есть; ровно одно из n/s/b по типу t
  "agg_cnt": 1,                              // int >=1; если type=event: 1; если agg: число сырых событий в агрегации
  "win": 0                                   // если type=event: 0; если type=agg: окно агрегации в ms (>0)
}
```

Контракт транспорта (общая JSON-схема выше используется и для HTTP, и для NATS):
- HTTP ingest:
  - endpoint: `POST <ingest_path>` (по умолчанию `/ingest`).
  - batch endpoint: `POST <ingest_path>/batch` (по умолчанию `/ingest/batch`).
  - body:
    - single: один JSON-объект события.
    - batch: JSON-массив событий (минимум 1 элемент).
  - нормальный ответ: `202 Accepted`.
  - ошибки:
    - `405 Method Not Allowed` — метод не `POST`;
    - `400 Bad Request` — невалидный JSON/контракт события/пустой batch;
    - `503 Service Unavailable` — событие валидно, но внутренняя обработка недоступна.
- NATS ingest (только multi-instance, `service.mode=nats`):
  - transport: JetStream queue consumer (`ingest.nats`) с фиксированными runtime-параметрами:
    - `subject = "alerting.events"`
    - `stream = "ALERTING_EVENTS"`
    - `consumer_name = "alerting-ingest"`
    - `deliver_group = "alerting-workers"`
  - state backend (NATS KV) также фиксирован в runtime:
    - `tick_bucket = "tick"`, `data_bucket = "data"`
    - `delete_consumer_name = "alerting-resolve"`
    - `delete_deliver_group = "alerting-resolve"`
    - `delete_subject_wildcard = "$KV.tick.>"`
  - payload в `msg.Data`:
    - single: один JSON-объект события;
    - batch: JSON-массив событий (минимум 1 элемент).
  - обработка:
    - decode/validation error: сообщение ACK и отбрасывается (без redelivery);
    - push/processing error: сообщение NAK и redelivery по policy consumer (`ack_wait_sec`, `nack_delay_ms`, `max_deliver`).

## Алертинг
## Ключ алерта
KEY - ключ алерта. Нужен для агрегации и дедупликации.
Формат: rule/<sanitized_rule_name>/<sanitized_var>/<sha1(key.from_tags)>, где sha1 считается по канонической строке tag=value для тегов из key.from_tags в стабильном порядке.
key.from_tags определяет набор тегов, которые входят в вычисление ключа.
Если в коннфиге алерта нет обязательного тега из key.from_tags, событие игнорируется и пишется warning в лог.

##  Фильтрация событий
К выделению алерта допускаются событияE, которые прошли фильтр. Фильтр позвожмен по типу события E.type ∈ rule.types (например ["event","agg"]), таги tags имена и значения переменных.
При этом:
- tags фильтруется только allow-фильтр по ключам и значениям. Если значение не попадает в allow, событие игнорируется
- Имена и значения переменных:
value.t="n|s|b"
  t - тип переменной:
  - n: float64
  - s: string
  - b: bool

  Возможные операции на каждый тип данных:
   - value.t="n": == != > >= < <=
   - value.t="s": == != in prefix match *
   - оператор * использует glob-маску (*, ?), сравнение без учета регистра
   - value.t="b": == !=


Если фильтрация не прошла — событие не влияет на алерт.
## Состояния алерта
```
pending -> firing -> resolved
```
pending поддерживается как явное состояние в карточке алерта и в логике переходов.
Переход pending -> firing управляется через [rule.<rule_name>.pending].enabled и [rule.<rule_name>.pending].delay_sec.
Для перехода pending -> firing условие правила должно выполняться непрерывно весь интервал pending.delay_sec.
Уведомление о входе в pending управляется параметром notify.on_pending.
Повторные уведомления в firing отправляются по notify.repeat_every_sec (в текущем базовом примере: каждые 300 секунд).
Повтор firing ведется отдельно по каждому исходящему каналу.
Доставка работает в двух режимах:
- notify.queue.enabled=true: событие -> общее построение Notification -> enqueue в notify.queue -> worker рендерит шаблон из [[notify.<channel>.name-template]] -> транспорт канала.
- notify.queue.enabled=false: событие -> общее построение Notification -> немедленная отправка через dispatcher/transport в процессе manager (без отдельной очереди).
При notify.queue.dlq=true недоставленные jobs (permanent error / исчерпан max_deliver) пишутся в отдельный DLQ stream.
В правиле хранится только привязка [[rule.<rule_name>.notify.route]] (channel + template).
При переходе в resolved объект алерта удаляется из runtime/KV-представления.
При переходе firing -> resolved всегда отправляется одно resolved-уведомление (независимо от notify.repeat_on).
- best-effort per-channel гарантируется в queue-режиме.
- В direct-режиме отправка fail-fast: ошибка канала прерывает dispatch текущего уведомления.
Для канала Telegram resolved отправляется с reply на первое сообщение открытия алерта (pending или firing, что было отправлено первым).
Для каналов Jira/YouTrack firing выполняет create issue, а resolved закрывает/transition ту же задачу по сохраненному external_ref (alert card в NATS KV).
При ошибке отправки выполняются ретраи по notify.<channel>.retry с логированием каждой ошибки/попытки (в dispatcher, в обоих режимах доставки).

### Out-of-order события
Обработка окон ведется по времени обработки now.

Рекомендуемые защитные параметры:
- max_late_ms — если now - dt > max_late_ms, событие игнорируется и логируется (warn).
- max_future_skew_ms — если dt > now + max_future_skew_ms, событие игнорируется и логируется (warn).

## Тип алерта "CountTotal"
```
Количество событий(N) прошедших фильтр. Без ограничений на период времени

UP:
- накопительный счётчик S[KEY]
- при каждом срабатывании: S[KEY] += agg_cnt
- если S[KEY] >= N → FIRING(KEY) (или PENDING(KEY) -> FIRING(KEY), если включен pending)

DOWN:
- обновляем last_seen[KEY] = now на каждом срабатывании
- если now - last_seen[KEY] >= silence_sec + hysteresis_sec → RESOLVED(KEY)

Обязательные параметры (конфиг):
- raise.n >= 1
- resolve.silence_sec >= 0
- resolve.hysteresis_sec >= 0

Запрещённые параметры (конфиг):
- raise.tagg_sec
- raise.missing_sec
```
Пример: configs/alerts/rules.count_total.toml.

## Тип алерта "CountWindow"
```
Количество событий(N) прошедших фильтр за период времени  tagg_sec ( time aggregation. скользящее окно)

UP:
- W[KEY] = sum(agg_cnt) по событиям за последние tagg_sec секунд
- если W[KEY] >= N → FIRING(KEY) (или PENDING(KEY) -> FIRING(KEY), если включен pending)

DOWN:
- если now - last_seen[KEY] >= silence_sec + hysteresis_sec → RESOLVED(KEY)
- обычно resolve.silence_sec = raise.tagg_sec, но допускается отдельное значение

Обязательные параметры (конфиг):
- raise.n >= 1
- raise.tagg_sec > 0
- resolve.silence_sec >= 0
- resolve.hysteresis_sec >= 0

Запрещённые параметры (конфиг):
- raise.missing_sec
```
Пример: configs/alerts/rules.count_window.toml.

##  Тип алерта "MissingHeartbeat"
```
Отсутствие событий прошедших фильтр за период времени missing_sec

UP:
- только после первого принятого heartbeat события
- если now - last_seen[KEY] >= missing_sec → FIRING(KEY) (или PENDING(KEY) -> FIRING(KEY), если включен pending)

DOWN:
- при новом heartbeat начинается окно восстановления resolve.hysteresis_sec
- если heartbeat стабилен весь интервал resolve.hysteresis_sec → RESOLVED(KEY)
- если heartbeat снова пропал до конца окна, восстановление сбрасывается (флап-защита)

Обязательные параметры (конфиг):
- raise.missing_sec > 0

Параметры resolve (конфиг):
- resolve.hysteresis_sec >= 0 (0 = мгновенный resolved на первом heartbeat)

Запрещённые параметры (конфиг):
- raise.n
- raise.tagg_sec
- resolve.silence_sec
```
Пример: configs/alerts/rules.missing_heartbeat.toml.

## Паттерны написания правил алертов

### Общий каркас правила
```toml
[rule.<rule_name>]
# Тип алерта (count_total | count_window | missing_heartbeat).
alert_type = "count_total"

[rule.<rule_name>.match]
# Разрешенные типы событий для этого правила.
type = ["event"]
# Разрешенные var для этого правила.
var = ["errors"]
# Allow-only фильтр тегов: событие подходит только если все указанные теги совпали.
tags = { dc = ["dc1"], service = ["api"] }

[rule.<rule_name>.key]
# Теги, формирующие уникальность alert_id (кардинальность алертов).
from_tags = ["dc", "service", "host"]

[rule.<rule_name>.raise]
# Параметры взведения (зависят от alert_type).

[rule.<rule_name>.resolve]
# Параметры опускания (зависят от alert_type).
# Для всех типов доступен общий гистерезис.
hysteresis_sec = 0

[rule.<rule_name>.pending]
# Включить/выключить стадию pending перед firing.
enabled = false
# Время удержания условия в pending до перехода в firing.
delay_sec = 300

[[rule.<rule_name>.notify.route]]
# Канал доставки (telegram | mattermost | jira | youtrack | http).
channel = "telegram"
# Имя шаблона из [[notify.<channel>.name-template]].
template = "tg_default"
```

Практика анти-флап:
- если алерт «дергается» между `firing/resolved`, сначала увеличивайте `resolve.hysteresis_sec`;
- если алерт часто кратковременно входит в `firing`, включайте `pending.enabled=true` и настраивайте `pending.delay_sec`.

# Доставка уведомлений
Общая схема доставки:
- при notify.queue.enabled=true: event -> alert decision -> Notification -> notify.queue (JetStream) -> delivery worker -> transport channel;
- при notify.queue.enabled=false: event -> alert decision -> Notification -> dispatcher -> transport channel.
- Глобальные настройки доставки: configs/alerts/base.toml ([notify], [notify.queue]).
- Маршрутизация задается в правилах через [[rule.<rule_name>.notify.route]] (channel, template).
  Актуальные rule-файлы: configs/alerts/rules.count_total.toml, configs/alerts/rules.count_window.toml, configs/alerts/rules.missing_heartbeat.toml.
- В исходящем уведомлении alert_id обязателен и равен ключу алерта (rule/<sanitized_rule>/<sanitized_var>/<sha1(key.from_tags)>).
- Отдельный notification_id не используется.

**Обязателен только любой один канал доставки**

# Каналы доставки
## Telegram
- Транспортный конфиг: configs/alerts/notify.telegram.toml.
- Логика: firing отправляет сообщение открытия алерта; resolved отправляется reply на первое сообщение этого алерта (по сохраненному message_id).

## Mattermost
- Транспортный конфиг: configs/alerts/notify.mattermost.toml.
- Логика: firing создает post в Mattermost; resolved публикуется в thread этого алерта через root_id (ссылка на post.id сообщения firing).

## Jira
- Транспортный конфиг: configs/alerts/notify.jira.toml.
- Логика: firing создает задачу, resolved закрывает/переводит задачу по сохраненному external_ref.
- Планы: ввести матрицу ответственности по типу инцидента. 

## YouTrack
- Транспортный конфиг: configs/alerts/notify.youtrack.toml.
- Логика аналогична Jira: firing create, resolved close/resolve через сохраненный external_ref.
- Планы: ввести матрицу ответственности по типу инцидента. 

# Конфиг (TOML)
## Структура конфигов
- configs/alerts/base.toml — глобальный конфиг сервиса:
  - [service] — process/runtime настройки (name, reload_*, resolve_scan_interval_sec, runtime state limits).
  - [ingest.http] — HTTP server/ingest (listen, health_path, ready_path, ingest_path, max_body_bytes, enabled).
  - [log.*], [ingest.nats], [notify] — остальные подсистемы.
- configs/alerts/rules.count_total.toml — правила типа count_total ([rule.<rule_name>], [rule.<rule_name>.*]).
- configs/alerts/rules.count_window.toml — правила типа count_window.
- configs/alerts/rules.missing_heartbeat.toml — правила типа missing_heartbeat.
- configs/alerts/notify.telegram.toml — transport-конфиг Telegram ([notify.telegram], [notify.telegram.retry], [[notify.telegram.name-template]]).
- configs/alerts/notify.mattermost.toml — transport-конфиг Mattermost ([notify.mattermost], [notify.mattermost.retry], [[notify.mattermost.name-template]]).
- configs/alerts/notify.jira.toml — transport-конфиг Jira ([notify.jira], [notify.jira.auth], [notify.jira.create], [notify.jira.resolve], [[notify.jira.name-template]]).
- configs/alerts/notify.youtrack.toml — transport-конфиг YouTrack ([notify.youtrack], [notify.youtrack.auth], [notify.youtrack.create], [notify.youtrack.resolve], [[notify.youtrack.name-template]]).
- configs/live.telegram.env — env для live e2e теста Telegram.
- deploy/nats/* — bootstrap/verify/cleanup скрипты для stream/KV/consumers.


## Глобальный конфиг сервиса
### Multi-instance
```toml
# base.toml
[service]
# Логическое имя процесса (для логов/диагностики).
name = "alerting"
# Явный multi-instance режим.
mode = "nats"
# Включает периодический hot reload конфигов.
reload_enabled = true
# Интервал проверки изменений конфигов (сек).
reload_interval_sec = 3
# Интервал одного шага фонового цикла обработки таймерных переходов.
# Интервал фонового тика движка алертов (сек).
# Используется для resolve/repeat/timer-driven логики.
resolve_scan_interval_sec = 1

[log.console]
# Включить вывод логов в stdout/stderr.
enabled = true
# Уровень логирования: debug|info|warn|error|panic.
level = "info"
# Формат для консоли:
# - line: человекочитаемый короткий формат
# - json: структурированные логи
# Формат вывода: line|json.
format = "line"

[log.file]
# Включить запись логов в файл.
enabled = true
# Уровень логирования: debug|info|warn|error|panic.
level = "info"
# Формат для файла:
# - line: текстовый
# - json: удобный для парсеров/агентов сбора
# Формат вывода: line|json.
format = "line"
# Путь к файлу логов (создается автоматически при запуске).
path = "./alerting.log"

[ingest.http]
# Включить прием событий по HTTP.
enabled = true
# HTTP bind-адрес сервиса (хост:порт).
listen = "127.0.0.1:8080"
# Health endpoint: жив ли процесс.
health_path = "/healthz"
# Ready endpoint: готов ли сервис принимать трафик.
ready_path = "/readyz"
# HTTP endpoint входящих событий.
ingest_path = "/ingest"
# Жесткий лимит размера тела POST /ingest.
# Максимальный размер тела запроса (байт).
max_body_bytes = 1048576


[ingest.nats]
# В multi-instance режиме NATS ingest включен.
enabled = true
# Список URL NATS/JetStream (драйвер поддерживает несколько адресов).
# Этот же список используется и для state backend (отдельной state-секции нет).
url = ["nats://127.0.0.1:4222"]
# Количество параллельных ingest workers внутри одного процесса.
# Рекомендуется >1 для high-load NATS ingest.
workers = 4
# Ack timeout (сек): если не ack вовремя, сообщение будет redelivered.
ack_wait_sec = 30
# Задержка перед NAK redelivery (мс) при ошибке обработки.
nack_delay_ms = 1000
# Максимум попыток доставки сообщения:
# -1 = бесконечно, >0 = конечный лимит.
# Лимит redelivery:
# -1 = бесконечно, >0 = конечное число доставок.
max_deliver = -1
# Максимум unacked сообщений у consumer.
max_ack_pending = 4096

[notify]
# Глобальные правила повторных уведомлений.
# Детали конкретного транспорта задаются в transport-файлах.

# Включить повторы уведомлений.
repeat = true
# Интервал повтора (сек).
repeat_every_sec = 300
# В каких состояниях повторять.
# В MVP обычно повторяем только firing.
repeat_on = ["firing"]
# Режим ключа повтора:
# - true: отдельно по каждому каналу доставки
# - false: общий счетчик повторов на alert_id
# true: таймеры повторов считаются отдельно по каждому каналу.
# false: общий repeat-таймер на alert.
repeat_per_channel = true
# Отправлять ли уведомление при входе в pending.
on_pending = false

[notify.queue]
# Асинхронная очередь доставки уведомлений (отдельно от ingest path).
# Если включено, manager только публикует jobs, а отправка по каналам идет worker-ом.
enabled = true
# Включить fixed DLQ для permanent/max-deliver ошибок.
dlq = true
# Ack timeout (сек): если worker не ack вовремя, job будет redelivered.
ack_wait_sec = 30
# Задержка перед NAK redelivery (мс) при ошибке отправки.
nack_delay_ms = 1000
# Максимум попыток доставки job:
# -1 = бесконечно, >0 = конечный лимит.
max_deliver = -1
# Максимум unacked jobs у consumer.
max_ack_pending = 4096
```
rule_name специальных ограничений формата не имеет; используется значение, прошедшее валидацию TOML-конфига и проверку уникальности.


### Single-instance
Самый компактный способ работы сервиса но и самый ненадежный. Недоступность одного сервиса  - это недоступность всей функции алертинга. Применять только для алертинга некртичных контуров.
```toml
# base.toml
[service]
# Режим single-instance (без NATS).
mode = "single"
name = "alerting"
reload_enabled = true
reload_interval_sec = 3
resolve_scan_interval_sec = 1

[log.console]
enabled = true
level = "info"
format = "line"

[log.file]
enabled = true
level = "info"
format = "line"
path = "./alerting.log"

[ingest.http]
enabled = true
listen = "127.0.0.1:8080"
health_path = "/healthz"
ready_path = "/readyz"
ingest_path = "/ingest"
max_body_bytes = 1048576

[notify]
repeat = true
repeat_every_sec = 300
repeat_on = ["firing"]
repeat_per_channel = true
on_pending = false

```

### Конфиги уведомлений
- Канальные конфиги хранятся в configs/alerts/.
- `[notify.<channel>.retry]` — ретраи задаются отдельно для каждого канала, без глобального retry.
- `[[notify.<channel>.name-template]]` — шаблоны канал-специфичны и выбираются из `[[rule.<rule_name>.notify.route]]` (`channel + template`).

### Hot reload
Конфиги могут применяться без перезапуска сервиса

# Подготовка контура
## NATS
Актуально только для режима работу multi-instance 
Перед запуском должен быть доступен NATS Server с включенным JetStream.

Стандарт эксплуатации: NATS контур готовится полностью скриптом bootstrap
(ingest stream/consumer, notify queue stream/consumer, notify DLQ stream, state KV/consumer).

Полный bootstrap (обязательный шаг):
1. Поднимите NATS Server с JetStream (если еще не запущен):

2. Подготовьте env для deploy-скриптов:
```bash
cp ./deploy/nats/.env.example ./deploy/nats/.env
```
3. Создайте весь контур (streams/consumers/KV):
```bash
./deploy/nats/bootstrap.sh ./deploy/nats/.env
```
4. Проверьте, что контур создан:
```bash
./deploy/nats/verify.sh ./deploy/nats/.env
```
Ожидаемый результат: NATS deploy verification OK ....

tick KV stream дополнительно нормализуется сервисом при старте (AllowMsgTTL=true, SubjectDeleteMarkerTTL>0) для корректной TTL delete-marker логики.

## Запуск сервиса алертинга
```bash
alerting --config-dir ./configs/alerts
```

# Мониторинг сервиса
Контракт HTTP endpoints
- GET /healthz:
  - нормальный ответ: `200 OK`, body: `ok`.
  - используется для liveness (процесс жив).
- GET /readyz:
  - нормальный ответ: `200 OK`, body: `ready`.
  - пока сервис не готов/уходит в shutdown: `503 Service Unavailable`, body: `not-ready`.
- POST /ingest[/batch]:
  - нормальный ответ: `202 Accepted` для валидного события.
  - поддерживается batch endpoint `POST <ingest_path>/batch` (например `/ingest/batch`), нормальный ответ: `202 Accepted`.
  - ошибки:
    - `405 Method Not Allowed` — если метод не `POST`.
    - `400 Bad Request` — невалидный JSON/схема события или невалидный batch.
    - `503 Service Unavailable` — событие декодировано, но внутренняя обработка недоступна (push error).
