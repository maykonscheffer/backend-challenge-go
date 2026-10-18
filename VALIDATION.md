# Execuções verificadas — 17/10/2026

## Opcionais acrescentados — 18/10/2026

Benchmark separado: smoke com -race (100 chamadas, quatro clientes/carteiras) aprovado;
dois cenários de 1000 chamadas, oito clientes e 16/1 carteiras aprovados, cada um com
900 operações únicas, 100 replays, zero falhas e reconciliação exata. Metodologia, ambiente,
latências, throughput, conflitos e backlog estão em BENCHMARK.md. Relatórios brutos em
test-results/benchmark-smoke.json, benchmark-many.json e benchmark-shared.json.

Dashboard separado: go test -race -count=1 -v ./cmd/dashboard passou (1,031s), incluindo
consulta OIDC/métricas reais das três APIs, restrição Host/Origin e ausência de segredo
na resposta. Node --check aprovou o JavaScript extraído de index.html. Não houve inspeção
visual automatizada em navegador. Go vet ./cmd/dashboard ./cmd/benchmark passou.

Nenhuma alteração nas regras financeiras ou endpoints da aplicação. A suíte funcional
anterior não foi repetida; a validação foi restrita às novas ferramentas.

## Correções após auditoria — 18/10/2026

Atualização deste bloco: após adicionar tipos concretos de eventos, Debit/Credit com
Money e erros classificáveis, a suíte completa foi executada novamente com -race e
passou (integração 21,302s; messaging 2,185s; bootstrap 1,046s). Três APIs foram
reconstruídas antes da execução. Log: test-results/domain-fixes-tests.txt.
Os novos unitários validam rejeição de moeda sem alteração, débito/crédito, errors.Is/As,
política de zero dos cinco tipos e referência inválida sem panic.

- FAILED pelo transporte SQS real: SQLSTATE 22000 provocado no ledger, rollback financeiro,
  inbox/FAILED persistidos e encaminhamento à DLQ com motivo PERMANENT_DATA_ERROR.
  O mesmo envelope foi reenviado com outro dedup ID de transporte: duas entregas na inbox,
  mesmo transactionId, um evento de falha e saldo/ledger preservados. Passou com -race.
- Dois publishers ativos: barreira mantém o primeiro com lock de um evento; o segundo
  publica outra carteira e não seleciona o registro reservado. Após liberação, ambos
  concluem com uma publicação/tentativa cada. PostgreSQL e SQS reais; passou com -race.
- Regressão TestCrashRecovery, TestSQSIntegration e TestOutboxPublication passou com -race
  (messaging: 2,225s; integração: 11,000s).
- Logs locais: test-results/dlq-publishers.txt e test-results/dlq-regression.txt.

Não foi repetida a instalação limpa neste bloco. As outras pendências
de AUDIT.md permanecem abertas; nenhuma publicação GitHub foi feita.

Ambiente: Windows com Docker Desktop/WSL2, 32 GiB RAM; testes Go em Linux no Compose.

| Verificação | Resultado |
|---|---|
| Compilação local Go 1.27.0 e build Docker | Passou |
| `go vet ./...` após integração SQS | Passou |
| `go test -race -count=1 -v ./...` no container | Passou |
| `go test -race -count=1 -v ./test -run TestSQSIntegration` após adicionar retry | Passou; 3 cenários SQS |
| 50 duplicatas entre três processos independentes | Um débito, mesmo transactionId |
| Duas apostas de 80.00 sobre 100.00 | Uma processada, uma rejeitada, saldo 20.00 |
| Autorização e isolamento entre provedores | Passou |
| Reversões, LOSS, paginação e reconciliação | Passou |
| Proteções do banco contra saldo negativo, divergência e alteração de ledger | Passou |
| Carteira bloqueada não impede outra carteira | Passou |
| Interrupção das três APIs e replay após reinício, antes da integração SQS | Passou; script disponível |
| HTTP e SQS concorrentes para a mesma operação | Mesmo resultado, um efeito financeiro |
| Três mensagens SQS com envelope idêntico e dedup IDs distintos | Três entregas na inbox, um débito |
| Envelope reutilizado com payload diferente | DLQ, saldo preservado |
| SQS primeiro, HTTP depois | Replay do resultado persistido |
| Lock SQL mantido além do timeout | Inbox ausente durante falha; retry concluiu com um débito |

Registros locais: `test-results/messaging-tests.txt`, `messaging-retry.txt` e `restart.txt`.
Não versionados; os comandos de reprodução estão no README.

Limites da evidência: `-race` instrumenta os pacotes executados pelo processo de testes;
os binários das APIs no Dockerfile atual não são compilados com `-race`. Concorrência entre
processos é verificada por resultados e invariantes no PostgreSQL, não pelo race detector.
A seção abaixo registra ampliações posteriores, incluindo os testes de interrupção. Esses resultados não certificam conformidade integral com o desafio nem prontidão para produção.

## Publicador da outbox — 17/10/2026

Suíte completa `docker compose --profile test run --rm tests go test -race -count=1 ./...`
aprovada após migration 003 e rebuild das três APIs. Integração terminou em 9,097s.
Registro local: `test-results/publisher-tests.txt`.

- Eventos de abertura e aposta chegaram ao SQS na ordem por carteira, iguais ao snapshot SQL.
- Replay HTTP não criou eventos extras; os quatro eventos foram marcados publicados.
- Em schema isolado no PostgreSQL real, lock no primeiro evento impediu ultrapassagem
  pelo seguinte, enquanto outra carteira avançou. Rollback liberou o trabalho.
- Fila inexistente produziu falha real no broker, preservou pendência e incrementou
  tentativa; backoff impediu nova seleção imediata. Restaurada a fila e antecipado
  o horário de retry pelo teste, os eventos foram publicados.

O teste de locks usa transações controladas, não morte de processo. O teste de retry
simula endereço de fila inválido, não indisponibilidade total do broker. Não confundir
essas evidências com os cenários de interrupção exata ainda pendentes.

## Referências pendentes — 17/10/2026

- Suíte completa com `-race` passou após migration 004 (integração: 13,413s).
- REFUND antes da BET: HTTP 202, replay com mesmo ID e nenhuma alteração de saldo/versão.
- Duas reversões pendentes da mesma aposta: exatamente uma processada; outra ALREADY_REVERSED.
- Chave reutilizada com valor diferente: conflito; transação terminal permanece imutável no banco.
- Referência ausente: tentativa durável registrada sem efeito financeiro.
- Pendência já expirada inserida pelo teste: REJECTED/REFERENCE_NOT_FOUND e um evento de rejeição.
  O teste não espera 24h reais; usa prazo vencido persistido na preparação.
- Teste adicional SQS passou (3,313s): inbox e pendência confirmadas, replay HTTP compartilhado,
  retomada após BET e exatamente um crédito de devolução.
- `scripts/test-restart.ps1` passou: kill das três APIs, replay financeiro preservado e
  pendência anterior ao kill concluída após chegada da BET, mesmo transactionId e ledger correto.

Registros locais: `test-results/pending-tests.txt`, `pending-sqs.txt`, `pending-restart.txt`.
O teste de restart não injeta falha precisamente entre commit e ACK ou entre envio e commit.

## Auditoria ampliada — 17/10/2026

Última suíte completa: `go test -race -count=1 ./...` passou; integração HTTP/SQL/SQS
16,113s, recuperação 2,180s e lifecycle Fx 1,041s. `go vet ./...` terminou sem diagnósticos.
Logs: `test-results/audit-tests.txt`, `audit-vet.txt`, `iam-tests.txt`, `crash-tests.txt`.

- Eventos: campos financeiros/versão da carteira, abertura versão 1, evento de pendência,
  correlação não vazia e tentativa de alteração do snapshot rejeitada pelo PostgreSQL.
- IAM: chamadas permitidas funcionaram; ingresso lendo fila, ingresso enviando à saída,
  serviço enviando à entrada e leitor enviando à entrada foram negados. Credencial
  desconhecida e segredo incorreto também foram negados pelo broker.
- Crash consumidor: subprocesso saiu após commit e antes do delete; reentrega elevou
  inbox.deliveries a 2, preservando saldo 95,00 e exatamente dois lançamentos.
- Crash publicador antes/depois do envio: lock liberado após morte do processo;
  outro publisher assumiu a linha e confirmou publicação com o mesmo eventId.
- Fx: construção, início e encerramento reais; pool sem conexões após OnStop.
- Money: aritmética exata, moedas incompatíveis, valores não inicializados, overflow
  de soma/subtração/negação e débito MinInt64 rejeitado sem corromper carteira.
- Migrations: banco temporário real, up/up/down/up, versão final 5.
- Expiração: token assinado pelo Keycloak real, verificado com relógio avançado em 48h,
  resultou em HTTP 401. Não foi necessário aguardar a duração real do token.
- Métricas: endpoint autenticado expôs os novos indicadores.

Limites: fila FIFO pode suprimir republicação imediata; não foi aguardada a janela de
cinco minutos. Os testes de crash usam o mesmo código Go em subprocessos de teste,
não SIGKILL de containers precisamente nesses pontos. O teste separado de restart
usa kill real das três APIs. Não houve teste prolongado de carga nem indisponibilidade
total sustentada de PostgreSQL/SQS. Perfis IAM compartilham um volume de teste local.

## Instalação limpa — 17/10/2026

`scripts/test-clean.ps1` passou em projeto Docker gerado, com volumes novos para banco,
SQS, credenciais IAM e caches Go. Imagens/camadas de build locais foram reutilizadas.
Suíte completa com `-race`: integração 17,123s; testes IAM positivos/negativos passaram.
Os recursos temporários foram removidos; o projeto principal permaneceu preservado.
Log local: `test-results/clean-compose.txt`.

## Interrupções de infraestrutura — 17/10/2026

`scripts/test-outages.ps1` passou na repetição sem intervenção manual.
Log conclusivo: `test-results/outages-confirmation.txt`.

- MiniStack parado: aposta HTTP confirmada, saldo 90,00 e dois eventos pendentes no banco.
- MiniStack restaurado: as APIs mantiveram suas credenciais e publicaram os dois eventos.
- PostgreSQL parado: nova aposta retornou HTTP 503.
- PostgreSQL restaurado: mesma operação processada uma vez; replay em outra API preservou
  identidade; reconciliação confirmou saldo 85,00 e três lançamentos (abertura + duas apostas).

A primeira execução revelou perda de IAM no encerramento do emulador com o timeout padrão.
Foi necessário restaurar esse ambiente manualmente; seu log `outages.txt` não comprova
recuperação automática do broker. A correção foi `stop_grace_period: 60s`, seguida de nova
execução independente e aprovada. Paradas foram feitas com docker compose stop/start,
não SIGKILL da infraestrutura nem simulação de perda de disco.

## Estados e timestamps — 17/10/2026

Migration 006 aplicada sobre o banco existente. A primeira tentativa foi revertida por
SQLSTATE 55006 (eventos de triggers adiados antes de ALTER TABLE). Após ajustar a ordem
das verificações, atualização e suíte completa com -race passaram: integração 18,270s.
O teste de banco temporário também executou up/up/down/up até a versão 6.

Novas evidências: reidratação sem efeitos, estados terminais bloqueados, entidades não
inicializadas rejeitadas, LOSS sem alterar horário da carteira, pendência sem completed_at,
conclusão com horário preenchido e replay preservando esse horário. Tentativa direta de
alterar auditoria de transação terminal foi rejeitada pelo PostgreSQL.
Log local: test-results/state-audit-tests.txt.

## FAILED persistido — 17/10/2026

Migration 007 e suíte completa com -race aprovadas; integração 20,741s. Go vet sem diagnósticos.
Logs locais: test-results/failed-final-tests.txt e failed-vet.txt.

- Trigger temporário de teste em PostgreSQL real injeta SQLSTATE 22000 somente para
  operações identificadas do cenário, após UPDATE do saldo e antes do INSERT no ledger.
- Reversão pendente termina FAILED/PERMANENT_DATA_ERROR, com completed_at e exatamente
  um WagerTransactionFailed. Saldo permanece 90,00 e ledger contém apenas abertura e BET.
- Nova BET síncrona com a mesma falha termina FAILED e retorna HTTP 500; replay preserva ID.
- ProcessMessage (caso de uso compartilhado pelo consumidor) confirma inbox e FAILED juntos;
  replay HTTP lê o mesmo resultado. Este cenário específico não injeta falha via transporte SQS.
- Tentativa de reabrir FAILED é recusada pelo banco.
- Teste unitário distingue classe 22 de timeout, cancelamento, deadlock, serialização,
  constraints, permissão/configuração e erro de conexão.

A entidade FAILED e seu evento são gravados numa nova transação depois do rollback da
financeira. Não se promete gravar auditoria enquanto o próprio banco está indisponível.

## Carteira e ledger encapsulados — 17/10/2026

Migration 008 aplicada sobre o histórico existente. Suíte completa com -race passou;
integração 43,300s. Migrations up/up/down/up em banco novo chegaram à versão 8.
Log: test-results/aggregates-tests.txt.

Testes unitários verificaram identidade da carteira, timestamps consistentes, restauração
sem mudança de estado, ausência de alteração em movimento zero, atualização em débito,
ledger com equação correta e rejeição de direção/valor/moeda inválidos. A suíte financeira
exercitou a criação e leitura usando essas entidades, preservando todas as invariantes.
Os lançamentos antigos receberam UUIDs na migration sem UPDATE dos valores financeiros.

## Validação limpa final — registrada em 18/10/2026

A versão com migrations 001 a 008 passou novamente em projeto Compose temporário,
com banco, SQS, credenciais IAM e caches Go vazios. A suíte completa com -race passou
(integração 21,071s), assim como todos os testes IAM. As imagens/camadas Docker existentes
puderam ser reutilizadas. Os recursos temporários foram removidos ao término.
Log local: test-results/final-clean.txt. Gofmt -l não apontou arquivos pendentes.
A busca textual básica por formatos conhecidos de tokens/chaves privadas não encontrou
ocorrências nos arquivos de entrega; isso não substitui auditoria de segurança completa.

## Money, observabilidade, shutdown e transação — 18/10/2026

Suíte completa com -race aprovada após rebuild das três APIs: bootstrap 1,068s,
domínio 1,010s, messaging 3,231s, store 1,008s e integração 21,982s.
Log: test-results/transaction-model-tests.txt. Execução anterior deste bloco em
test-results/mandatory-tests.txt também passou (integração 20,673s).

Novas evidências: JSON/Format rejeitam Money/moeda inválidos; diferença monetária
protegida contra overflow; contador SQL cresce em lock timeout real; métricas SQS
incluem duração e replays; SIGTERM após commit conclui ACK e deixa a próxima mensagem;
startup recusa schema antigo e fecha pool; transação mantém metadados/Money;
abertura interna e reidratação possuem testes de invariantes.

A última chamada de testes focados/vet foi recusada antes da execução. O pequeno
ajuste posterior à suíte completa foi retirado; mantida a versão já testada.
Go vet final e instalação limpa da versão atual não foram executados neste bloco.
Sem ZIP e sem publicação GitHub.

## Fechamento solicitado — 18/10/2026

Proteção de reidratação de OPENING implementada: resultado diferente do valor original
é rejeitado com ErrInvalid. Teste específico com -race aprovado em 1,010s.
Go vet ./... concluído sem diagnósticos. Logs: opening-final.txt e final-vet.txt.

scripts/test-clean.ps1 aprovou a versão final com volumes novos de dados, caches e
credenciais, reutilizando imagens/camadas disponíveis. Suíte completa com -race:
bootstrap 1,122s; domain 1,010s; messaging 3,280s; store 1,009s; integração 21,455s.
Testes IAM positivos/negativos aprovados. Log: test-results/delivery-clean.txt.
O projeto temporário wallet-clean-b85e996fe5 e seus volumes foram removidos.
As pendências de vet e execução limpa registradas acima estão encerradas.
Nenhum ZIP criado e nenhuma publicação GitHub realizada.

## Conferência visual do dashboard — 18/10/2026

Painel aberto em http://127.0.0.1:8090 no navegador. Inspeção visual confirmou
layout legível, métricas das três APIs e gráfico. O botão Pausar mudou o estado
para atualização pausada; Retomar voltou a consultar as APIs. A leitura observada
mostrou backlog e atraso da outbox iguais a zero. Isso encerra a pendência visual
registrada anteriormente; não houve alteração de código nem repetição da suíte.

## OpenTelemetry HTTP e SQS/outbox — 18/10/2026

SDK/exportador OTLP HTTP 1.47.0, Jaeger local 2.22.0 e migration 009.
Validação específica com -race:
- HTTP: propagação do pai remoto e rota parametrizada sem dados da URL (1,013s).
- Telemetry: desativado por padrão; 2.000 spans com coletor 503 e fila limitada
  sem bloquear o produtor; shutdown tolera falha de exportação (1,034s).
- TestTracingEndToEnd: APIs reais, PostgreSQL, SQS e consulta do Jaeger. HTTP e SQS
  preservam pai e filhos da outbox, contexto persistido, payload, replay e saldo.
  Execução da integração: 3,431s.
- TestOutboxTraceTransport: mensagem realmente recebida no SQS com contexto do
  produtor; snapshot/contexto originais preservados. Incluído no pacote messaging.

Regressões afetadas executadas uma vez com -race: startup/lifecycle e versão de
schema; envelopes, crash de consumer/publisher e SIGTERM; migrations up/down/up
em banco novo; finanças/idempotência, eventos, pendências, FAILED/DLQ e publicação
concorrente/retry. Bootstrap: 1,095s; messaging: 3,387s; integração: 24,441s.
Go vet ./... passou sem diagnósticos.

Não foi repetida a execução completa com volumes novos: ela pertence ao bloco
anterior. A migration nova foi validada no teste de banco novo e no upgrade do
ambiente local existente. Nenhum ZIP, commit ou publicação realizado neste bloco.

## Revisão documental e suíte completa final — 18/10/2026

Código financeiro e funcionalidades inalterados. Containers das três APIs
reconstruídos e recriados com o código da branch main (ea43321); apenas documentos
receberam ajustes locais. Tracing habilitado nas APIs e Jaeger disponível.
Volumes existentes preservados; esta execução não equivale a um ambiente de volumes novos.

Comandos executados, em ordem (na raiz do projeto):

```sh
docker compose -f compose.yaml -f compose.tracing.yaml up -d --build --force-recreate app1 app2 app3 jaeger
docker compose -f compose.yaml -f compose.tracing.yaml --profile test run --rm --no-deps -e TEST_TRACE_QUERY_URL=http://jaeger:16686 tests go test -race -count=1 ./...
docker compose -f compose.yaml -f compose.tracing.yaml --profile test run --rm --no-deps tests go vet ./...
```

Build/recriação concluídos. Suíte completa: código de saída 0, sem falhas.
O endpoint TEST_TRACE_QUERY_URL ativa o teste real de HTTP/SQS/outbox no Jaeger.

| Pacote | Resultado |
|---|---|
| cmd/dashboard | PASS — 1,687s |
| internal/bootstrap | PASS — 1,089s |
| internal/domain | PASS — 1,014s |
| internal/httpapi | PASS — 1,019s |
| internal/messaging | PASS — 3,386s |
| internal/store | PASS — 1,015s |
| internal/telemetry | PASS — 1,034s |
| test (integração) | PASS — 54,845s |

cmd/api, cmd/benchmark e migrations não têm testes próprios nesses pacotes;
a migração é exercitada pelos testes de integração. Go vet: saída 0, sem diagnósticos.
Scripts separados de IAM, reinício e indisponibilidade não foram repetidos neste bloco;
suas evidências anteriores permanecem acima. O benchmark também não foi repetido.

Logs locais excluídos do Git: test-results/final-review-build.txt,
final-review-tests.txt e final-review-vet.txt. Links locais do README e da matriz
REQUIREMENTS.md conferidos, sem destinos ausentes.
