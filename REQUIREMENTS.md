# Requisitos e evidências

Mapa para revisão da implementação. Cada linha aponta para o código e para testes
ou scripts reproduzíveis; não representa uma nota atribuída pelo avaliador.
Resultados efetivamente executados: [VALIDATION.md](VALIDATION.md).

| Requisito do enunciado | Implementação | Evidência |
|---|---|---|
| OIDC e isolamento entre provedores | [HTTP e autorização](internal/httpapi/server.go), [Fx/IdP](internal/bootstrap/app.go) | `TestFinancialIntegration/authorization and provider isolation` em [integration_test.go](test/integration_test.go); `TestExpiredIdentityToken` em [infrastructure_test.go](test/infrastructure_test.go) |
| Credenciais e políticas do broker | [Provisionamento IAM](deploy/broker-init.py) | [Teste positivo/negativo IAM](deploy/test-broker-iam.py); execução registrada em VALIDATION.md |
| Dinheiro exato, escala, moedas e overflow | [Money](internal/domain/money.go), [parsing e regras](internal/domain/domain.go) | [money_test.go](internal/domain/money_test.go), [domain_test.go](internal/domain/domain_test.go) |
| Carteira, transações, estados e reidratação | [Domínio](internal/domain/domain.go), [transação](internal/domain/transaction.go) | [aggregates_test.go](internal/domain/aggregates_test.go), [transaction_test.go](internal/domain/transaction_test.go) |
| BET, WIN, LOSS, REFUND e ROLLBACK | [Regras](internal/domain/domain.go), [caso de uso e persistência](internal/store/store.go) | [rules_test.go](internal/domain/rules_test.go), `TestFinancialIntegration` em [integration_test.go](test/integration_test.go) |
| OPENING atômico e saldo inicial zero | [Abertura](internal/store/store.go), [constraints](migrations/001_up.sql) | [events_test.go](test/events_test.go), [transaction_test.go](internal/domain/transaction_test.go) |
| Ledger imutável e integridade no banco | [Ledger](internal/domain/ledger.go), [schema e triggers](migrations/001_up.sql), [UUID](migrations/008_up.sql) | `database guards` em [integration_test.go](test/integration_test.go), [audit_test.go](test/audit_test.go) |
| Idempotência persistente HTTP/SQS | [Store](internal/store/store.go), [consumidor](internal/messaging/consumer.go) | 50 duplicatas em [integration_test.go](test/integration_test.go); reentregas e cruzamento HTTP/SQS em [messaging_test.go](test/messaging_test.go) |
| Concorrência entre três instâncias | [Locks por carteira](internal/store/store.go), [Compose](compose.yaml) | Disputa 100/80/80 e carteiras independentes em [integration_test.go](test/integration_test.go) |
| API, paginação e reconciliação | [Handlers](internal/httpapi/server.go), [consultas](internal/store/store.go) | `reversals loss and ledger pagination` e reconciliações em [integration_test.go](test/integration_test.go) |
| Referências pendentes com retry/TTL | [Worker](internal/messaging/pending.go), [retomada](internal/store/pending.go) | [pending_test.go](test/pending_test.go), [messaging_test.go](test/messaging_test.go) |
| Inbox, retry, DLQ e FAILED | [Consumidor](internal/messaging/consumer.go), [falha durável](internal/store/failure.go) | [messaging_test.go](test/messaging_test.go), [failed_test.go](test/failed_test.go) |
| Outbox, eventos tipados e publicação após commit | [Eventos](internal/domain/events.go), [persistência](internal/store/events.go), [publicador](internal/messaging/publisher.go) | [events_test.go](test/events_test.go), [publisher_test.go](test/publisher_test.go) |
| Recuperação de crash e encerramento SIGTERM | [Consumidor/publicador](internal/messaging), [Fx](internal/bootstrap/app.go) | [recovery_test.go](internal/messaging/recovery_test.go), [app_test.go](internal/bootstrap/app_test.go) |
| Reinício e indisponibilidade de infraestrutura | [Persistência](internal/store), [workers](internal/messaging) | [test-restart.ps1](scripts/test-restart.ps1), [test-outages.ps1](scripts/test-outages.ps1); resultados anteriores em VALIDATION.md |
| Logs, métricas e health checks | [HTTP](internal/httpapi/server.go), [métricas](internal/httpapi/metrics.go), [consumidor](internal/messaging/consumer.go) | `TestMetricsContract` e `TestSQLLockConflictMetric` em [infrastructure_test.go](test/infrastructure_test.go) |
| Migrations, execução local e ciclo de vida | [Migrations](migrations), [Compose](compose.yaml), [Fx](internal/bootstrap/app.go) | `TestMigrationsFreshDatabase`, [app_test.go](internal/bootstrap/app_test.go), [test-clean.ps1](scripts/test-clean.ps1) |

## Opcionais e limites

Benchmark, dashboard e OpenTelemetry estão documentados em [BENCHMARK.md](BENCHMARK.md),
[DASHBOARD.md](DASHBOARD.md) e [TRACING.md](TRACING.md). Partidas dobradas não foram
implementadas e não são obrigatórias.

A cobertura não é uma prova de ausência de defeitos nem uma validação para produção.
Limitações de recuperação, FIFO, broker e testes estão em [AUDIT.md](AUDIT.md) e
[ARCHITECTURE.md](ARCHITECTURE.md). Scripts de interrupção e IAM são verificações
separadas: executar `go test ./...` não os executa automaticamente.
