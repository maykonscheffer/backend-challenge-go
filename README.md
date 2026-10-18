# Carteiras e apostas — desafio backend em Go

API financeira com Go 1.27.0, Uber Fx, pgx/PostgreSQL, Keycloak e SQS via MiniStack. Implementa processamento HTTP e assíncrono, consistência financeira, idempotência e recuperação entre processos.
A cobertura demonstrada e as limitações estão em [REQUIREMENTS.md](REQUIREMENTS.md) e [VALIDATION.md](VALIDATION.md).

Enunciado original: [CHALLENGE.md](CHALLENGE.md).

## Implementado

Opcionais disponíveis: [benchmark HTTP reproduzível](BENCHMARK.md),
[dashboard local de métricas](DASHBOARD.md) e [OpenTelemetry HTTP/SQS/outbox](TRACING.md).
O tracing é desativado por padrão e pode ser ativado com coletor Jaeger local.
Partidas dobradas não estão implementadas.

- Abertura e consulta de carteira; BET, WIN, LOSS, REFUND e ROLLBACK, com espera durável por referências.
- Valores em centavos (`int64`/`BIGINT`), sem ponto flutuante; BRL, USD e EUR com escala 2.
- Lock por carteira; saldo, operação, ledger e registros de outbox no mesmo commit.
- Idempotência persistente por provedor e chave, hash canônico e resultado original no replay.
- Ledger append-only, constraints financeiras, reconciliação em snapshot único e paginação.
- OIDC/JWKS com validação de assinatura RS256, emissor, audiência e expiração; permissões por role.
- Três containers da API com memória e pools independentes; logs JSON, health e métricas básicas.
- Consumidor SQS FIFO via MiniStack, inbox atômica, retry, DLQ e deduplicação cruzada HTTP/SQS.

## Limitações importantes

**Validação:** suíte completa com `-race` e execução com volumes novos aprovadas antes do tracing. Após sua inclusão, testes específicos, regressões afetadas e `go vet` passaram. Consulte REQUIREMENTS.md e VALIDATION.md para a cobertura e as limitações.
Referência ausente fica em `PENDING_REFERENCE`; após 24h sem conclusão, termina rejeitada.
Os eventos são publicados em `wallet-events.fifo` via outbox. Readiness verifica
PostgreSQL/migration e acesso às filas SQS de entrada e saída.
Veja [ARCHITECTURE.md](ARCHITECTURE.md) para decisões e demais desvios.

## Pré-requisitos e inicialização

Docker com Compose v2, engine Linux. No Windows, Docker Desktop com WSL2. Go local é opcional:
build e testes podem rodar inteiramente nos containers. Portas locais: 8080–8082 (APIs),
8180 (Keycloak) e 15432 (PostgreSQL). Todos os binds ficam em `127.0.0.1`.

```sh
docker compose up --build -d
docker compose ps
```

O Compose cria banco e login restrito, importa o realm/clients/roles do Keycloak, aplica
a migration como dono do schema e inicia três APIs como usuário de banco sem privilégios
administrativos. Keycloak pode levar cerca de um minuto para emitir tokens na primeira execução.
Nenhuma conta AWS ou assinatura adicional é necessária. O MiniStack tem imagem fixada por
digest, persiste estado em volume e não expõe porta ao host. SDK e filas são acessados pela
rede interna do Compose. O enforcement IAM está ativado e possui testes de permissões positivas e negativas.

Credenciais em `deploy/realm.json`, `deploy/init.sql` e Compose são **fictícias, apenas locais**.
Não use esta configuração em produção. O `.env.example` serve para executar a API diretamente
no host; Go não lê `.env` automaticamente: exporte as variáveis na sua shell.

## Demonstração no PowerShell

```powershell
$base = 'http://localhost:8080'
$tokenURL = 'http://localhost:8180/realms/wallet/protocol/openid-connect/token'
$internal = (Invoke-RestMethod -Method Post -Uri $tokenURL -Body @{
  grant_type='client_credentials';client_id='internal-service';client_secret='local-internal-service-secret'
}).access_token
$provider = (Invoke-RestMethod -Method Post -Uri $tokenURL -Body @{
  grant_type='client_credentials';client_id='provider-a';client_secret='local-provider-a-secret'
}).access_token
$player = [guid]::NewGuid().ToString()
$wallet = Invoke-RestMethod -Method Post -Uri "$base/wallets" -ContentType 'application/json' `
  -Headers @{Authorization="Bearer $internal"} -Body (@{
    playerId=$player;initialBalance=@{amount='100.00';currency='BRL'}
  } | ConvertTo-Json)
$operation = @{
  providerId='provider-a';externalTransactionId=[guid]::NewGuid().ToString()
  playerId=$player;walletId=$wallet.id;roundId='round-1';gameId='demo';kind='BET'
  money=@{amount='25.00';currency='BRL'}
} | ConvertTo-Json
$key = [guid]::NewGuid().ToString()
Invoke-RestMethod -Method Post -Uri "$base/wagering/transactions" -ContentType 'application/json' `
  -Headers @{Authorization="Bearer $provider";'Idempotency-Key'=$key} -Body $operation
# Repetir a chamada anterior devolve o mesmo transactionId e saldo 75.00, sem novo débito.
Invoke-RestMethod -Method Post -Uri "$base/wallets/$($wallet.id)/reconciliation" `
  -Headers @{Authorization="Bearer $internal"}
```

Outro provedor de teste: `provider-b` / `local-provider-b-secret`.
Console Keycloak: http://localhost:8180 — `admin` / `local-admin-password`.

## Endpoints

| Método e rota | Identidade |
|---|---|
| POST /wallets | interna |
| GET /wallets/{walletId} | interna |
| GET /wallets/{walletId}/ledger?limit=50&cursor=... | interna |
| POST /wallets/{walletId}/reconciliation | interna |
| POST /wagering/transactions | provedor; Idempotency-Key obrigatório |
| GET /wagering/transactions/{transactionId} | provedor proprietário |
| GET /providers/{providerId}/wagering/transactions/{externalId} | provedor proprietário |
| GET /health/live e /health/ready | público |
| GET /metrics | interna |

Respostas: 201 abertura, 200 processamento/replay/consulta, 400 entrada inválida,
401 credencial inválida/ausente, 403 operação não permitida, 404 recurso não encontrado
(inclui consulta a transação de outro provedor), 409 conflito, 422 rejeição financeira persistida,
503 infraestrutura indisponível. Erros de transporte usam `{"code":"..."}`; rejeições incluem
`transactionId`, `status`, `balance`, `code` e `idempotentReplay`.

## Testes

Com a infraestrutura acima ativa:

```sh
docker compose --profile test run --rm tests
docker compose --profile test run --rm tests go vet ./...
```

O primeiro comando executa `go test -race -count=1 ./...` em Linux, com banco/IdP reais
e as três instâncias. No host:

```sh
go test ./...
go vet ./...
```

Sem `TEST_API_URLS`, a suíte de integração é **explicitamente ignorada**; os testes de domínio
continuam executando. Para integração no host, defina `TEST_API_URLS` com as três URLs separadas
por vírgula, `TEST_TOKEN_URL` e `TEST_DATABASE_URL` (dono do schema, apenas para testar proteções).
No Windows `-race` pode exigir compilador C; o container de testes evita essa dependência local.

Os testes criam carteiras/IDs únicos e não apagam dados existentes. Não rodar contra produção.
Os cenários cobrem 50 duplicatas, disputa 100/80/80, carteiras independentes, replays com saldo
original, conflitos, autorização, reversões, paginação e proteções do schema.

Teste adicional de interrupção abrupta e replay persistente (PowerShell):

```powershell
./scripts/test-restart.ps1
```

Esse script encerra as três APIs com `docker compose kill`, reinicia e repete uma operação
confirmada antes da interrupção. Não simula o ponto exato entre commit e resposta ou ACK SQS.

## Migrations e encerramento

```sh
docker compose run --rm migrate migrate up
docker compose stop app1 app2 app3
# ATENÇÃO: down remove as tabelas e todos os dados desta aplicação.
docker compose run --rm migrate migrate down
docker compose run --rm migrate migrate up
docker compose up -d app1 app2 app3
```

O migrador usa lock consultivo transacional e tabela de versão; `up` repetido não reaplica.
Migrations: 001 financeiro, 002 inbox, 003 publicador, 004 referências pendentes, 005 metadados de eventos e proteção da outbox, 006 auditoria temporal, 007 falhas permanentes, 008 identidade estável do ledger. `up` aplica as pendentes; `down` reverte todas. A reversão 004 recusa execução enquanto existirem operações PENDING_REFERENCE, evitando descartar trabalho durável.
`docker compose down` encerra os containers preservando os volumes;
`docker compose down -v` também apaga os dados e caches locais.

## Arquivos

`internal/domain`: regras puras; `internal/store`: transação SQL e consultas;
`internal/httpapi`: contratos e autorização; `internal/bootstrap`: composição Fx;
`migrations`: schema embutido no binário; `test`: integração entre processos.

## Entrada SQS

As três instâncias provisionam idempotentemente `wager-transactions.fifo` e
`wager-transactions-dlq.fifo`. A fila de entrada usa redrive com `maxReceiveCount=5`.
Envie o envelope abaixo pelo SDK AWS, com `MessageGroupId=walletId` e
`MessageDeduplicationId` único por envio (o `messageId` do envelope identifica a entrega
duravelmente na aplicação). O teste varia o ID de deduplicação SQS para comprovar a inbox.

```json
{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-10-17T12:00:00Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "bet-123",
    "idempotencyKey": "provider-a:bet-123",
    "playerId": "UUID_DO_JOGADOR",
    "walletId": "UUID_DA_CARTEIRA",
    "roundId": "round-1",
    "gameId": "demo",
    "kind": "BET",
    "money": {"amount":"10.00","currency":"BRL"}
  }
}
```

`test/messaging_test.go` contém exemplos executáveis de envio e consumo via SDK.
Corpos inválidos, conflitos de identidade e carteiras inexistentes vão à DLQ. Rejeições
financeiras confirmadas são removidas normalmente. Visibility timeout de 30s, processamento
limitado a 12s, long polling de 5s; retry em 2/4/8/16/32s conforme contagem de recebimento.
Em SIGTERM, para novas buscas, conclui a mensagem corrente e aguarda o worker antes do pool.
Se não conseguir concluir, a mensagem fica disponível após o timeout ou tem sua visibilidade
liberada. Logs e métricas registram recebimentos, retries e transferências à DLQ.

Variáveis adicionais para rodar no host: `AWS_REGION`, `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, `SQS_ENDPOINT`, `SQS_ALLOWED_PROVIDERS`. O endpoint precisa ser
alcançável pelo host; o Compose fornecido mantém o broker acessível apenas entre containers.

## Publicação da outbox

As três instâncias publicam eventos em `wallet-events.fifo`. Cada tentativa bloqueia
uma linha da outbox com `FOR UPDATE SKIP LOCKED`, mantendo o lock durante o envio
(limitado a 5s; tentativa completa limitada a 10s). Carteiras distintas podem avançar
em paralelo; um evento posterior da mesma carteira aguarda a confirmação do anterior.
Falhas mantêm o evento pendente com backoff de 2s até 64s, sem limite de tentativas.
Isso preserva eventos, mas um evento permanentemente inválido bloqueia sua carteira
até intervenção operacional. O serviço não oferece interface administrativa para isso.

O payload publicado é o snapshot do commit. `MessageGroupId` é a carteira e
`MessageDeduplicationId` é o `eventId`. A entrega é **at-least-once**: falha após o envio
e antes do commit pode reenviar o evento. A deduplicação temporária do SQS não substitui
uma inbox durável no consumidor de saída. Morte do processo libera o lock pelo PostgreSQL;
a interrupção real entre envio e commit foi testada em subprocesso.

## Referências pendentes

Uma referência ausente, ou ainda em `PENDING_REFERENCE`, gera resposta HTTP 202 com
`status=PENDING_REFERENCE` e `code=REFERENCE_PENDING`. A operação preserva identidade,
chave, hash e payload; não altera saldo, versão nem ledger. O replay consulta seu estado
persistido atual. Depois da conclusão, devolve sempre o resultado terminal armazenado.

Três workers procuram pendências vencidas para nova tentativa a cada 500ms, em lotes de
até 16. Cada operação adquire o lock de sua carteira e revalida estado e horário. A primeira
retomada ocorre após 2s; novas esperas crescem de 2s até 60s. O prazo máximo é de 24h a
partir do aceite, sem renovação em replay ou retry. Expirado o prazo, a próxima execução
rejeita com `REFERENCE_NOT_FOUND` e grava o evento de rejeição. O prazo vale mesmo se a
referência só estiver disponível quando o worker executar após o vencimento.

Referência terminal rejeitada causa `REFERENCE_NOT_PROCESSED`; uma referência processada
passa novamente por todas as validações financeiras. A transição final, saldo, ledger e
outbox compartilham o commit. Falha de infraestrutura faz rollback e deixa a pendência
para outro ciclo. A inbox SQS pode ser confirmada junto com a pendência: a continuidade
passa a depender do banco, sem exigir que a mensagem permaneça na fila.

O script `scripts/test-restart.ps1` também verifica uma pendência sobrevivendo ao encerramento
abrupto das três APIs. Referências faltantes rejeitadas antes da migration 004 permanecem
terminais; a migration não reinterpreta resultados históricos.

## IAM local e contratos de eventos

MiniStack executa com `AUTH=true`. `broker-init` provisiona três usuários IAM e grava
credenciais geradas somente no volume local `brokercredentials` (fora do repositório).
As APIs usam `AWS_PROFILE=wallet-service`, com consumo da entrada e envio para saída/DLQ;
`trusted-ingress` pode enviar apenas à entrada; `event-reader` pode consumir apenas a saída.
A credencial raiz local `local/local` fica no provisionamento e nos testes administrativos.
O arquivo de perfis é compartilhado entre containers locais: este Compose é ambiente de
avaliação, não isolamento de segredos adequado a um ambiente de produção.

`trusted-ingress` representa um produtor interno confiável que autentica os provedores
antes de enviar; não se deve distribuir essa credencial aos provedores externos.
O consumidor valida a allowlist, mas não deriva providerId da assinatura AWS da mensagem.
A allowlist não substitui autenticação do provedor. O broker não é exposto no host.

Teste de permissões, inclusive credencial desconhecida e assinatura errada:

```sh
docker compose run --rm --entrypoint python broker-init /bootstrap/test-broker-iam.py
```

Os quatro eventos exigidos possuem envelope tipado com eventId, eventType, aggregateId,
correlationId, causationId opcional, occurredAt UTC, version e data. Tipos/construtores:
`internal/domain/events.go`. WalletBalanceChanged inclui direção, valor, saldo anterior,
saldo posterior e versão da carteira. Abertura usa versão 1. Pendência emite
WagerTransactionPendingReference somente no primeiro aceite, não em cada tentativa.
HTTP preserva X-Correlation-Id (ou gera um); SQS usa messageId como correlação e causa.
Esses IDs são persistidos para sobreviver à retomada de pendências. Snapshots anteriores
à migration 005 não são reescritos e podem usar o contrato simplificado histórico.
Rejeições expõem `failureCode`; `code` é mantido como alias de compatibilidade.

## Testes de falha e diagnóstico

```sh
docker compose --profile test run --rm tests go test -race -count=1 -v ./internal/messaging -run TestCrash
docker compose --profile test run --rm tests go vet ./...
```

Os testes encerram subprocessos reais com os.Exit(91), sem executar defers, após commit
antes de DeleteMessage e antes/depois do envio SQS. Pontos de injeção são callbacks de
teste: não há endpoint nem variável de ambiente que os ative na aplicação normal.
Filas e schema de outbox são isolados; PostgreSQL e MiniStack são reais. FIFO pode suprimir
a republicação imediata, mas o evento mantém eventId e a nova instância confirma a outbox.
Não se alega execução prolongada além da janela de deduplicação do SQS.

Métricas autenticadas em /metrics incluem resultados por status (gauges do banco),
backlog/idade da outbox, recebimentos SQS, retry, DLQ, publicações, conflitos HTTP, replays,
latência HTTP acumulada e divergências. Contadores em memória são por instância e reiniciam
com ela; gauges SQL são globais e não devem ser somados entre réplicas. Latência usa float
somente para segundos de tempo; valores financeiros continuam inteiros/strings.


## Instalação limpa em ambiente isolado

No PowerShell, com Docker disponível no PATH:

```powershell
./scripts/test-clean.ps1
```

O script gera um nome de projeto temporário, sobe o Compose com volumes vazios
(incluindo dados, credenciais e caches de Go), executa a suíte com `-race` e os testes IAM.
Usa `compose.clean.yaml` para não publicar portas nem conflitar com a aplicação principal.
Ao terminar, remove somente os containers e volumes desse projeto temporário.
Imagens e camadas de build já disponíveis no Docker podem ser reutilizadas.

## Interrupção e recuperação da infraestrutura local

```powershell
./scripts/test-outages.ps1
```

Executar sem outros testes em paralelo. O script interrompe temporariamente MiniStack e
PostgreSQL do projeto principal, preservando volumes. Durante a queda do broker, confirma
uma aposta por HTTP e verifica eventos pendentes; após restaurá-lo, aguarda a publicação.
Durante a queda do banco, exige HTTP 503; depois reenvia a mesma operação, testa replay em
outra API e reconcilia saldo/ledger. A cláusula finally tenta restaurar serviços parados.

O MiniStack salva o estado no encerramento e precisa concluir a drenagem de requisições.
Por isso seu `stop_grace_period` é 60s. O prazo padrão de 10s causou perda das identidades
IAM no teste inicial; após o ajuste, a repetição passou sem reprovisionamento nem restart
das APIs. Isso não equivale a garantir persistência do emulador sob SIGKILL ou perda de disco.


## Falhas permanentes — FAILED

A política distingue rejeição de negócio, indisponibilidade transitória e falha permanente:

| Resultado | HTTP no envio/replay | Tratamento |
|---|---|---|
| PROCESSED | 200 | Resultado financeiro terminal |
| PENDING_REFERENCE | 202 | Worker retoma a mesma operação |
| REJECTED | 422 | Regra de negócio, com failureCode |
| FAILED | 500 com status/failureCode persistidos | Falha permanente, replay sem nova execução |
| Indisponibilidade transitória | 503 | Rollback e possibilidade de retry |

Somente exceções de dados PostgreSQL da classe SQLSTATE 22 são classificadas automaticamente
como permanentes nesta implementação. O código estável é PERMANENT_DATA_ERROR. Timeout,
conexão, deadlock, serialização, constraints, configuração/permissão e erro desconhecido não
viram FAILED por esgotar tentativas: permanecem erro transitório/operacional a investigar.
Essa é uma política conservadora e explícita, não classificação universal de toda infraestrutura.

A tentativa financeira falha primeiro e é revertida. Outra transação adquire o lock da carteira,
revalida idempotência/estado e grava FAILED com horário de conclusão e evento tipado adicional
WagerTransactionFailed. Não cria ledger nem altera saldo. Isso vale para o processamento direto
HTTP/SQS e para retomada de pendência. Se outro worker já concluiu, seu resultado prevalece.
Se o banco não conseguir gravar a própria falha, não há confirmação: o erro continua sujeito a retry.

Na entrada por mensagem, a inbox e FAILED são confirmados juntos;
o consumidor encaminha um resultado FAILED à DLQ com failureReason=PERMANENT_DATA_ERROR
antes de remover a mensagem de entrada. Se o envio à DLQ falhar, a entrada permanece
sujeita a reentrega; o replay consulta o resultado persistido sem repetir o efeito financeiro.
Reentregas de transporte podem duplicar notificações na DLQ, preservando o corpo original.
No worker de pendências,
a inbox do aceite original já está confirmada. Uma operação FAILED não pode ser reaberta;
uma referência FAILED causa rejeição REFERENCE_NOT_PROCESSED. Corrigir um defeito operacional
não modifica automaticamente registros terminais. Não há endpoint administrativo de reabertura.

A reversão da migration 007 é bloqueada se existirem registros FAILED, evitando perda ou
reclassificação silenciosa do histórico. Consultas GET continuam retornando 200 com o estado.

## Carteira e lançamentos no domínio

Wallet mantém privados identidade, jogador, saldo, versão e instantes de criação/atualização.
NewWallet e RestoreWallet distinguem criação de reidratação; dados inválidos são rejeitados.
Uma movimentação aceita altera saldo/versão/atualização; uma operação sem movimentação
não altera esses campos. O PostgreSQL continua sendo a autoridade para os timestamps persistidos.
As respostas de carteira incluem createdAt e updatedAt.

WalletLedgerEntry é imutável e valida identidade, moeda, valor positivo, direção e a equação
balanceAfter = balanceBefore ± money. Criação e leitura passam por essa validação, além das
constraints já existentes no banco. A migration 008 acrescenta UUID único aos lançamentos;
a sequência numérica continua sendo a chave de paginação. A consulta retorna id e balanceBefore
além dos campos anteriores. IDs de registros anteriores são gerados na migration, sem alterar
seus valores financeiros nem reemitir eventos.
