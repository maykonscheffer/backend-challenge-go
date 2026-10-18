# Decisões e limites

## Escopo

Solução que cobre processamento financeiro HTTP/SQS, recuperação e auditoria verificáveis. Não há alegação de atendimento integral ao enunciado,
nem de prontidão para produção.

## Dinheiro e domínio

Money tem campos privados e valida escala, formato canônico e moedas suportadas (BRL/USD/EUR).
Entrada usa string decimal estrita: `0.00`, sem sinais, expoentes, espaços ou zeros à esquerda.
Parsing remove o separador e usa ParseInt com detecção de overflow. Soma verifica MaxInt64;
débitos verificam saldo antes de alterar estado. Banco usa BIGINT. Wallet encapsula identidade, jogador, timestamps, saldo e versão; Operation é um comando de entrada, não uma entidade mutável. O domínio não importa
HTTP, Fx, SQS nem pgx.

## Atomicidade e concorrência

pgx v5 e SQL explícito. O método Process abre uma única transação READ COMMITTED e mantém
o mesmo pgx.Tx em todas as escritas. SELECT FOR UPDATE serializa apenas operações da mesma
carteira. Índices únicos protegem provedor/chave e provedor/ID externo mesmo quando duas
carteiras diferentes concorrem pela mesma identidade. Unique violation, deadlock ou
serialization failure reiniciam toda a tentativa até três vezes; esgotamento devolve 503.
Rejeições de domínio são confirmadas para auditoria; falhas de infraestrutura fazem rollback.
Não há commit intermediário PENDING. Há PENDING_REFERENCE durável, que pode permanecer pendente ou transitar para PROCESSED/REJECTED/FAILED. Estados terminais são imutáveis.

Saldo, versão, operação, lançamento e outbox são confirmados juntos. Abertura positiva cria
OPENING sem metadados de provedor, ledger e dois eventos; abertura zero não cria movimentação.
LOSS não muda saldo/versão nem cria ledger. A aplicação usa login sem propriedade do schema
e sem UPDATE/DELETE no ledger. Triggers impedem alteração do ledger e de transações terminais, exclusão e truncamento de registros
auditáveis. FK composta liga valor, moeda e carteira ao registro da operação. Trigger valida
encadeamento e constraint triggers adiados exigem saldo igual à soma do ledger no commit.
O custo dessa verificação por soma cresce com o histórico: escolhido por clareza e segurança,
sem alegar performance de produção. Não há benchmark de carga.

## Idempotência

Escopo `(providerId, Idempotency-Key)`; preserva a chave enviada. Identidade externa também
é única. Hash SHA-256 hexadecimal sobre JSON de maps (encoding/json ordena as chaves
recursivamente), contendo providerId, externalTransactionId, playerId, walletId, roundId,
gameId, kind, money.amount, money.currency e referência quando presente. UUIDs são normalizados
pela camada HTTP; dinheiro já é canônico. Escape HTML padrão do Go faz parte do algoritmo.
Headers e chave ficam fora do hash. Mesmo conteúdo com outra chave é conflito para o mesmo
ID externo. Replay lê o resultado JSON original, incluindo saldo e código de rejeição.
A autorização ocorre antes da busca; consultas SQL filtram provedor.

## Reversões

REFUND credita uma BET; ROLLBACK inverte BET/WIN/REFUND. Referência resolvida dentro do provedor,
com jogador, carteira, moeda e rodada iguais. Reversão integral e uma reversão processada por
alvo, reforçada por índice único parcial. Reversão de REFUND debita novamente, sem liberar
uma nova reversão da aposta original. WIN com referência exige BET processada do mesmo contexto.
Referência ausente ou pendente gera PENDING_REFERENCE sem efeito financeiro.
Workers retomam a mesma operação com backoff e TTL de 24h; veja detalhes no README.
A migration 004 permite alterar somente estado, resultado, referência resolvida e controle de retry enquanto a operação está pendente. Identidade e payload permanecem protegidos.

## Autenticação

Keycloak local com client_credentials. go-oidc verifica RS256, assinatura JWKS, issuer,
audience wallet-api e expiração. `azp` determina o provedor. Role `provider` habilita rotas
financeiras externas; `wallet-internal` habilita carteira, ledger, reconciliação e métricas.
Clientes internos não usam rotas de provedor. Não há emissão própria de tokens.
JWKS é consultado sob demanda, portanto startup não garante disponibilidade do IdP.
Secrets e HTTP local são apenas para demonstração; produção exigiria TLS e gestão de segredos.

## Consumidor SQS e inbox

SDK oficial AWS v2, MiniStack em container com persistência e digest fixo. Três consumidores
independentes buscam uma mensagem por vez com long polling. Fila FIFO por carteira. Não
dependemos da deduplicação temporária do broker: `messageId` do envelope é chave persistente
da inbox, com hash de negócio + chave + tipo + timestamp UTC. O hash financeiro é exatamente
o mesmo usado por HTTP; UUIDs são normalizados nas duas entradas.

ProcessMessage reutiliza Process e insere/atualiza inbox no mesmo pgx.Tx do efeito financeiro.
Se a transação falhar, o recebimento também é desfeito. Replays incrementam contador de
entregas e confirmam a inbox sem reaplicar dinheiro. Payload alterado para a mesma identidade
é conflito. DeleteMessage só ocorre depois de commit; falha no delete permite reentrega segura.
O consumidor possui allowlist de provedores e valida todo o domínio. A fila é canal interno
confiável; a allowlist NÃO autentica o remetente. Credenciais/políticas IAM de menor privilégio
com enforcement real foram configuradas e testadas: wallet-service, trusted-ingress e event-reader. A credencial de ingresso pertence a um produtor interno confiável, não aos provedores externos.

Long polling 5s; visibility 30s; operação limitada a 12s; lock SQL 5s. Falhas transitórias
mudam visibility com backoff limitado. Erros permanentes ou quinta tentativa são enviados
à DLQ antes do delete; redrive do broker é uma segunda proteção. Essa transferência é
at-least-once, portanto um consumidor de DLQ também deve deduplicar. OnStop cancela buscas,
aguarda operação corrente e só depois o Fx fecha o pool. Startup provisiona filas antes de
aceitar HTTP e readiness verifica banco e SQS. Métricas do consumidor são por instância.

## Eventos e infraestrutura não concluída

Outbox registra eventos no commit e publica em wallet-events.fifo. O envelope tipado contém
eventId, eventType, aggregateId, correlationId, causationId opcional, occurredAt, version e data
com snapshots protegidos pela migration 005. O publicador usa lock de linha com SKIP LOCKED e backoff; veja README.
FAILED é persistido para exceções de dados SQLSTATE 22 após rollback da tentativa financeira. Outras falhas de infraestrutura continuam sujeitas a retry. Há PENDING_REFERENCE com expiração e retomada durável.
A seleção da outbox só enxerga eventos confirmados. Falha após envio e antes do commit permite duplicatas com o mesmo eventId.

## Leituras e ciclo de vida

Ledger usa cursor opaco Base64URL com carteira e sequência, ordenado pela sequência crescente;
cursor não é credencial e é validado contra a carteira. Novos lançamentos podem aparecer em
páginas seguintes. Reconciliação usa um único SELECT e seu snapshot MVCC para saldo e soma.
Não altera registros; divergência gera resposta, log e contador. Contadores são locais à instância; gauges consultados do banco são globais.
Fx compõe config, pool, store, auth e servidor por construtores. Startup valida banco e migration.
Shutdown drena HTTP antes de fechar o pool; timeout força fechamento das conexões HTTP.

## Validação e lacunas

Suíte de integração roda contra três containers reais, PostgreSQL, Keycloak e MiniStack. Não usa mocks
para provar a correção financeira. Testes sem as variáveis de integração são marcados skip.
Há testes de duplicidade cruzada HTTP/SQS, múltiplos recebimentos comprovados na inbox,
conflito encaminhado à DLQ e timeout real no banco seguido de retry.
Foram testados encerramentos reais após commit/antes do ACK e antes/depois do envio na outbox, em subprocessos com dependências reais. Referências pendentes sobreviveram ao kill das três APIs. A composição Fx inicia e encerra com pool fechado. Token do IdP é rejeitado com relógio de validação avançado após a expiração. Consulte VALIDATION.md e REQUIREMENTS.md para limites e lacunas.




## Entidade de transação e auditoria temporal

WagerTransaction encapsula identidade, operação, estado e código de falha no domínio.
Criação e reidratação são separadas; reidratação valida consistência sem executar efeitos.
O store usa os métodos Process, Reject e WaitForReference antes de persistir o resultado.
Estados terminais recusam transições, com ErrInvalidTransition classificável por errors.Is.
Failed está integrado à persistência pela migration 007. A política e os códigos HTTP estão descritos no README: exceções de dados SQLSTATE 22 encerram a operação; timeout/conexão e erros não classificados não são finalizados por simples esgotamento de tentativas.

A migration 006 registra updated_at em carteiras/transações e completed_at em transações
terminais e inbox. Triggers SQL definem os horários. LOSS e replay não mudam updated_at da
carteira; uma transação terminal não pode ter seu horário reescrito. Na inbox, conclusão
significa tratamento durável, inclusive transferência para o worker de referências.

Para registros anteriores à migration 006, os horários acrescentados são preenchidos com
created_at; não se alega reconstrução de horários históricos desconhecidos. A conclusão
histórica da inbox também usa created_at. A migration realiza as verificações adiadas de
consistência antes do DDL, preservando as constraints financeiras.


## Ledger como entidade imutável

WalletLedgerEntry possui campos privados, construtores validados e apenas getters.
O repositório cria a entidade antes do INSERT e reidrata nas leituras, sem aplicar novos
movimentos. As constraints SQL continuam protegendo a mesma equação, moeda, cadeia e
unicidade contra alterações externas à aplicação. UUID identifica o lançamento; seq
permanece a ordenação estável do cursor. A migration 008 é reversível sem apagar movimentos.

# Ajustes de conformidade de 18/10/2026

O agregado Wallet recebe Money em Debit/Credit e valida a moeda antes de alterar o
estado; o método de delta bruto é privado. Regras financeiras retornam RuleError,
classificável por errors.Is/As, e a persistência converte-o para o failureCode estável.
Decide valida sua entrada antes de consultar referências. Os eventos possuem tipos
Go distintos por evento, mantendo os contratos JSON existentes.

No consumidor, FAILED confirmado é enviado à DLQ antes do delete da entrada. Falha
nessa transferência preserva a entrada para retry; o resultado persistido impede
reexecução financeira. Duplicatas de notificação na DLQ são possíveis após crashes.


## Complementos de conformidade — 18/10/2026

Money.JSON e Format retornam erro para moeda inválida; FromMinor restaura dinheiro
interno exato. A reconciliação calcula diferença com Money.Subtract, verificando overflow.
WagerTransaction carrega Money e snapshot de chave/hash, referência resolvida, tempos
e resultado. Reidratação de pendências usa metadados persistidos; PostgreSQL permanece
a autoridade dos timestamps gravados. OPENING é construído e processado no domínio.

Métricas locais por instância incluem conflitos SQL (23505, 40P01, 40001 e 55P03),
retries SQL efetivamente iniciados, duplicatas SQS e sum/count da duração de Handle,
incluindo transferência à DLQ/ACK. Esses contadores reiniciam com o processo.
O startup exige a versão final das migrations. Limites atuais constam em AUDIT.md.

## Tracing opcional

OpenTelemetry é injetado por Fx e desativado por padrão. HTTP cria spans de servidor;
SQS cria spans de consumidor, e o publicador cria spans de produtor. A migration 009
persiste traceparent/tracestate na outbox, sem alterar os snapshots JSON ou hashes
financeiros. O guard existente também impede alterar esses campos após o INSERT.
Exportação assíncrona limitada; coletor não faz parte da readiness. Detalhes e
limitações em TRACING.md.
