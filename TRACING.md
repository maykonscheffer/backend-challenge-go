# OpenTelemetry — opcional

Tracing HTTP e SQS/outbox com OpenTelemetry Go 1.47.0 e exportação OTLP/HTTP.
Desativado no Compose padrão. Não exige conta, assinatura ou serviço externo.

## Executar localmente

```sh
docker compose -f compose.yaml -f compose.tracing.yaml up -d --build
```

Abra http://127.0.0.1:16686 e selecione o serviço `wallet-api` no Jaeger.
Faça uma operação HTTP ou envie um envelope SQS e procure os spans:

- `POST /wagering/transactions`: recebimento HTTP;
- `wager-transactions process`: processamento de mensagem de entrada;
- `wallet-events publish`: cada tentativa de publicação da outbox.

O coletor Jaeger 2.22.0 usa memória; reiniciá-lo perde os traces, nunca dados
financeiros. A interface é publicada somente em loopback; OTLP fica na rede Docker.
Para voltar ao modo sem exportação:

```sh
docker compose -f compose.yaml -f compose.tracing.yaml stop jaeger
docker compose up -d app1 app2 app3
```

## Contexto e isolamento

`traceparent`/`tracestate` W3C são extraídos dos headers HTTP e dos atributos SQS.
A migration 009 guarda esses campos na outbox, separadamente do JSON do evento.
O publicador recupera o contexto e o envia nos atributos da mensagem de saída.
Retentativas conservam o contexto persistido e o eventId. A resolução posterior de
uma pendência recupera o contexto de seu primeiro evento quando disponível.
O encaminhamento à DLQ também leva o contexto nos atributos.

Não são coletados corpos, tokens, URLs com parâmetros, SQL, saldos ou dados do jogador.
HTTP registra rota parametrizada, método e status; não se propaga baggage.
O sampler respeita a decisão do pai; novos traces locais são amostrados integralmente.
Não há instrumentação individual de consultas SQL nem spans próprios do worker de pendências.

O envio é assíncrono, com fila de 512 spans, lotes de até 128 e timeout de um segundo.
Com fila cheia, traces podem ser descartados; operações financeiras não aguardam o
coletor. Shutdown tenta flush por até dois segundos, após parar os workers.
Falha de exportação não entra no resultado financeiro nem na readiness.

Configuração: `OTEL_ENABLED=true`, `OTEL_SERVICE_NAME=wallet-api` e
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://jaeger:4318/v1/traces`.
O override Compose configura as três APIs; fora dele, a ativação é explícita.

## Verificação

```sh
docker compose -f compose.yaml -f compose.tracing.yaml --profile test run --rm --no-deps -e TEST_TRACE_QUERY_URL=http://jaeger:16686 tests go test -race ./test -run TestTracingEndToEnd -v
```

O teste consulta o Jaeger real e verifica pai HTTP/SQS, filhos de publicação,
contexto persistido, payload separado, replay e reconciliação. Testes adicionais
cobrem atributos recebidos na fila de saída, rota sem dados privados, modo
desativado e coletor respondendo 503 com fila de exportação saturada.

Referências: [exportadores OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/exporters/)
e [Jaeger local](https://www.jaegertracing.io/docs/2.22/getting-started/).
