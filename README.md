# MXFlex 2

MXFlex предоставляет веб-сервер и API для обращения к некоторым функциям сервера Zultys MX.

## Авторизация пользователя

Перед обращением к API необходимо авторизовать пользователя и получить токен:

```http
POST /api/login HTTP/1.1
Content-Type: application/x-www-form-urlencoded; charset=utf-8
Host: localhost:8080
Content-Length: 34

login=user_login&password=password
```

Информация для авторизации пользователя передается в теле запроса:

- `login` - имя пользователя для авторизации на сервер MX
- `password` - пароль пользователя

При получении данных для авторизации происходит подключение к серверу MX и авторизация пользователя. После этого данное соединение сразу закрывается. При авторизации пользователя используются следующие параметры на сервер MX:

- `type` - `User`
- `platform` - `CRM`
- `version` - `1.0`

```xml
<loginRequest type="User" platform="CRM" version="1.0">
    <userName>user_login</userName>
    <pwd>W6ph5Mm5Pz8GgiULbPgzG37mj9g=&#xA;</pwd>
</loginRequest>
```

В случае успешной авторизации запускается мониторинг входящих звонков для данного пользователя, а в ответ возвращается авторизационный токен и информация о нем:

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Server: MXFlex/0.8
Content-Length: 274

{
    "token_type": "Bearer",
    "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1MDU0MTU1OTYsImV4dCI6IjMwOTUiLCJpYXQiOjE1MDU0MTE5ODYsIm14IjoiNjMwMjIiLCJzdWIiOjQzODg0ODUxMTQ3NDA2MTQ1fQ.zn_HXbEJeJnluJniqu8qMdKn05kdQw4agkpSxa-HcpY",
    "expires_in": 3600
}
```

Данный токен (`access_token`) представляет из себя JWT-токен и содержит в себе уникальный идентификатор пользователя (`sub`), его внутренний номер на сервере MX (`ext`), уникальный идентификатор сервера MX (`mx`), дату создания токена (`iat`) и дату, до которой он считается валидным (`exp`):

```json
{
  "mx": "63022",
  "ext": "3095",
  "sub": 43884851147406140,
  "iat": 1505411103,
  "exp": 1505414713
}
```

`expires_in` указывает в секундах время валидности токена, после истечения которого токен необходимо обновить. По умолчанию токен имеет ограничения по времени жизни **один час**, после чего требуется новая авторизация пользователя и получение нового ключа.

Токен подписывается уникальным ключом (**HS256**) и проверяется при каждом обращении к API. Ключ подписи **автоматически изменяется** при **каждом запуске сервера**, поэтому токены полученные ранее, после перезапуска сервера становятся не действительными и требуют повторной авторизации пользователя.

Множественные авторизации одного и того же пользователя приводят к генерации нескольких токенов авторизации, которые будут действительны и могут использоваться для одновременного доступа к функциям API.

Для обращения ко всем остальным функциям API требуется обязательная передача этого токена. Это можно сделать в заголовке авторизации HTTP. В качестве типа авторизации необходимо указать `Bearer`:

```http
GET /api/contacts HTTP/1.1
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1MDU0MTU1OTYsImV4dCI6IjMwOTUiLCJpYXQiOjE1MDU0MTE5ODYsIm14IjoiNjMwMjIiLCJzdWIiOjQzODg0ODUxMTQ3NDA2MTQ1fQ.zn_HXbEJeJnluJniqu8qMdKn05kdQw4agkpSxa-HcpY
Host: localhost:8080
```

Так же поддерживается передача токена в параметрах запроса URL:

```http
GET /api/contacts?access_token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1MDU0MTU1OTYsImV4dCI6IjMwOTUiLCJpYXQiOjE1MDU0MTE5ODYsIm14IjoiNjMwMjIiLCJzdWIiOjQzODg0ODUxMTQ3NDA2MTQ1fQ.zn_HXbEJeJnluJniqu8qMdKn05kdQw4agkpSxa-HcpY HTTP/1.1
Host: localhost:8080
```

Или токен может быть передан в теле запроса, когда используются методы `POST`, `PUT`, `PATCH`:

```http
POST /api/call HTTP/1.1
Content-Type: application/x-www-form-urlencoded; charset=utf-8
Host: localhost:8080
Connection: close
Content-Length: 226

to=%2B79031744445&access_token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1MDU0MTU1OTYsImV4dCI6IjMwOTUiLCJpYXQiOjE1MDU0MTE5ODYsIm14IjoiNjMwMjIiLCJzdWIiOjQzODg0ODUxMTQ3NDA2MTQ1fQ.zn_HXbEJeJnluJniqu8qMdKn05kdQw4agkpSxa-HcpY
```

## Окончание мониторинга звонков

Для прекращения мониторинга входящих звонков пользователя необходимо выполнить процедуру деавторизации, обратившись по адресу `/api/logout`. В запросе необходимо передать авторизационный токен:

```http
GET /api/logout HTTP/1.1
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1MDU0MTU1OTYsImV4dCI6IjMwOTUiLCJpYXQiOjE1MDU0MTE5ODYsIm14IjoiNjMwMjIiLCJzdWIiOjQzODg0ODUxMTQ3NDA2MTQ1fQ.zn_HXbEJeJnluJniqu8qMdKn05kdQw4agkpSxa-HcpY
Host: localhost:8080
```

После выполнения данного запроса мониторинг звонков пользователя преостанавливается до следующей авторизации пользователя.

## Мониторинг входящих звонков

Для мониторинга входящих звонков используется `/api/events`:

```http
GET /api/events?access_token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1MDU0MTU1OTYsImV4dCI6IjMwOTUiLCJpYXQiOjE1MDU0MTE5ODYsIm14IjoiNjMwMjIiLCJzdWIiOjQzODg0ODUxMTQ3NDA2MTQ1fQ.zn_HXbEJeJnluJniqu8qMdKn05kdQw4agkpSxa-HcpY HTTP/1.1
Accept: text/event-stream
Host: localhost:8080
```

В запроса необходимо передать токен для авторизации пользователя и указать, что поддерживается тип ответа `text/event-stream` в заголовке `Accept`. Ответы соответствуют спецификации W3C [Server-Sent Events](https://www.w3.org/TR/eventsource/).

На текущий момент отслеживаются следующие события:

- `OriginatedEvent`
- `DivertedEvent`
- `DeliveredEvent`
- `EstablishedEvent`
- `ConnectionClearedEvent`

> **Внимание!**
>
> На сегодняшний момент [браузеры Microsoft](https://developer.microsoft.com/en-us/microsoft-edge/platform/status/serversenteventseventsource/) [не поддерживают](http://caniuse.com/#feat=eventsource) спецификации Server-Sent Events. Для использования SSE в браузерах Microsoft можно воспользоваться библиотеками JavaScript [EventSource polyfill](https://github.com/Yaffle/EventSource)] или аналогичными.

В качестве отладки мониторинга событий можно использовать `curl`:

```shell
curl "http://localhost:8080/api/events?access_token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1MDU0MTU1OTYsImV4dCI6IjMwOTUiLCJpYXQiOjE1MDU0MTE5ODYsIm14IjoiNjMwMjIiLCJzdWIiOjQzODg0ODUxMTQ3NDA2MTQ1fQ.zn_HXbEJeJnluJniqu8qMdKn05kdQw4agkpSxa-HcpY" \
     -H "Accept: text/event-stream"

event: OriginatedEvent
data: {"callId":39,"deviceId":"3095","callingDevice":"3095","calledDevice":"79031744445","cause":"normal","callTypeFlags":36700160}

event: DivertedEvent
data: {"callId":39,"deviceId":"3095","divertingDevice":"3095","newDestination":"79031744445","cause":"normal","callTypeFlags":170917889}

event: DeliveredEvent
data: {"callId":39,"deviceId":"3095","globalCallId":"2808630435142227620","alertingDevice":"79031744445","callingDevice":"3095","calledDevice":"79031744445","localConnectionInfo":"alerting","cause":"normal","callTypeFlags":170917889,"cads":[{"name":"__FIRST_CALL_ID__","type":"string","value":"63022-00-0000D-6A4"}]}

event: EstablishedEvent
data: {"callId":39,"deviceId":"3095","globalCallId":"2808630435142227620","answeringDevice":"79031744445","answeringDisplayName":"79031744445","callingDevice":"3095","calledDevice":"79031744445","callingDisplayName":"3095","cause":"normal","callTypeFlags":170917889,"cads":[{"name":"__FIRST_CALL_ID__","type":"string","value":"63022-00-0000D-6A4"}]}

event: ConnectionClearedEvent
data: {"callId":39,"deviceId":"3095","releasingDevice":"3095","cause":"normal"}
```

## Адресная книга

Для получения адресной книги сервера MX можно воспользоваться запросом к `/api/contacts`:

```http
GET /api/contacts HTTP/1.1
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1MDU0MTU1OTYsImV4dCI6IjMwOTUiLCJpYXQiOjE1MDU0MTE5ODYsIm14IjoiNjMwMjIiLCJzdWIiOjQzODg0ODUxMTQ3NDA2MTQ1fQ.zn_HXbEJeJnluJniqu8qMdKn05kdQw4agkpSxa-HcpY
Host: localhost:8080
```

В ответ возвращается информация о пользователях, зарегистрированных на сервере MX, упорядоченная по их внутренним телефонным номерам:

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Server: MXFlex/0.8

{
    "contacts": [
        {
            "jid": "43884852633771555",
            "status": "LoggedOut",
            "firstName": "SMS",
            "lastName": "Gateway C73",
            "ext": "3010"
        },
        {
            "jid": "43884851338921185",
            "status": "LoggedOut",
            "firstName": "mxflex",
            "lastName": "mxflex",
            "ext": "3042"
        },
        {
            "jid": "43884852654574210",
            "status": "LoggedOut",
            "firstName": "Ilia",
            "lastName": "Test",
            "ext": "3043"
        },
        {
            "jid": "43884851428118509",
            "status": "LoggedOut",
            "firstName": "Peter",
            "lastName": "Hyde",
            "ext": "3044",
            "homePhone": "+1-202-555-0104",
            "cellPhone": "+1-512-555-0136",
            "email": "peterh@xyzrd.com",
            "did": "15125550136"
        },
        {
            "jid": "43884850646482261",
            "status": "LoggedOut",
            "firstName": "Mike",
            "lastName": "Flynn",
            "ext": "3055",
            "homePhone": "+1-202-555-0104",
            "cellPhone": "+1-512-555-0136",
            "email": "mikef@xyzrd.com"
        },
        {
            "jid": "43884850557879186",
            "status": "LoggedOut",
            "firstName": "Test",
            "lastName": "One",
            "ext": "3080"
        },
        {
            "jid": "43884851776746473",
            "status": "LoggedOut",
            "firstName": "Test",
            "lastName": "Two",
            "ext": "3081"
        },
        {
            "jid": "43884852542754454",
            "status": "LoggedOut",
            "firstName": "Test",
            "lastName": "Three",
            "ext": "3082"
        },
        {
            "jid": "43884852535898307",
            "status": "LoggedOut",
            "firstName": "dstest1",
            "lastName": "dstest1",
            "ext": "3091"
        },
        {
            "jid": "43884850939404214",
            "status": "LoggedOut",
            "firstName": "dstest2",
            "lastName": "dstest2",
            "ext": "3092",
            "cellPhone": "16693507465",
            "did": "16693507465"
        },
        {
            "jid": "43884850647480796",
            "status": "LoggedOut",
            "firstName": "Test",
            "lastName": "Admin",
            "ext": "3093"
        },
        {
            "jid": "43884852355777349",
            "status": "LoggedOut",
            "firstName": "Zultys",
            "lastName": "Test",
            "ext": "3094"
        },
        {
            "jid": "43884851147406145",
            "status": "Available",
            "firstName": "Dmitry",
            "lastName": "Sedykh",
            "ext": "3095",
            "cellPhone": "+79031744445",
            "email": "dmitrys@xyzrd.com"
        },
        {
            "jid": "43884851851343044",
            "status": "LoggedOut",
            "firstName": "Sergey",
            "lastName": "Kananykhin",
            "ext": "3096"
        },
        {
            "jid": "43884851514905017",
            "status": "LoggedOut",
            "firstName": "Test",
            "lastName": "Zultys",
            "ext": "3097"
        },
        {
            "jid": "43884851324615074",
            "status": "LoggedOut",
            "firstName": "John",
            "lastName": "Smith",
            "ext": "3098",
            "cellPhone": "12035160992",
            "did": "12035160992"
        },
        {
            "jid": "43884852031096113",
            "status": "Available",
            "firstName": "Maxim",
            "lastName": "Donchenko",
            "ext": "3099",
            "cellPhone": "+420720961083"
        }
    ]
}
```

## Звонок

Для осуществления звонка можно воспользоваться запросом к `/api/call`:

```http
POST /api/call HTTP/1.1
Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1MDU0MTU1OTYsImV4dCI6IjMwOTUiLCJpYXQiOjE1MDU0MTE5ODYsIm14IjoiNjMwMjIiLCJzdWIiOjQzODg0ODUxMTQ3NDA2MTQ1fQ.zn_HXbEJeJnluJniqu8qMdKn05kdQw4agkpSxa-HcpY
Content-Type: application/x-www-form-urlencoded; charset=utf-8
Host: localhost:8080
Content-Length: 14

from=79031234567&to=79037654321
```

В запросе можно использовать следующие параметры:

- `to` - номер телефона для звонка
- `from` - номер телефона пользователя

Если телефонный номер `from` не указан, то используется внутренний номер пользователя MX.

В ответ возвращается идентификатор звонка и другая информация:

```http
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Server: MXFlex/0.8
Content-Length: 107

{
    "call": {
        "callId": 38,
        "deviceId": "3095",
        "called": "+79031744445"
    }
}
```

## Call Hold

_Not implemented yet._

## Call Transfer

_Not implemented yet._

## Call Hangup

_Not implemented yet._

## Статические файлы

Для поддержки раздачи статических файлов их необходимо разместить в каталоге `html` рядом с сервером (используется текущий каталог). Эти файлы будут доступны по запросу `/<filename.ext>`. Файл с именем `index.html` отдается как корневой запрос к серверу `/`.

Например, можно разместить в каталоге `html` следующие файлы:

- `/index.html`
- `/scripts.js`
- `/style.css`
- `/favicon.ico`
- `/robots.txt`

Для доступа к этим файлам токен авторизации не требуется.

## Конфигурационный файл

По умолчанию используется конфигурационный файл с именем `mxflex.yaml`, в котором должна быть указана информация для подключения и авторизации серверного соединения MX:

```yaml
host: "localhost:8080"
mx: {
    addr: "89.185.246.134",
    login: "d3test",
    password: "981211"
}
```

Файл может быть в формате [YAML](http://www.yaml.org/spec/1.2/spec.html) или [JSON](http://www.json.org).

Если в качестве `host` указано имя сервера (не IP-адрес и не *.local), то сервер используется HTTP/2 с автоматическим получением сертификата TLS через Let's Encrypt.