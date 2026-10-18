# Conferência atual — 18/10/2026

Opcionais implementados e validados: benchmark HTTP (BENCHMARK.md), dashboard local
(DASHBOARD.md) e OpenTelemetry HTTP/SQS/outbox com Jaeger local (TRACING.md).
Partidas dobradas permanecem ausentes. A suíte completa final com tracing ativo e APIs reconstruídas passou com -race;
go vet também passou. Comandos e resultados em VALIDATION.md.

Fonte: https://github.com/junglegaming/backend-challenge-go/blob/main/README.md

Este documento substitui as marcações antigas da auditoria. A implementação abaixo passou pela suíte completa com -race, em PostgreSQL, Keycloak e MiniStack reais, com três APIs reconstruídas. Log: test-results/transaction-model-tests.txt (integração 21,982s). Não se atribui percentual ou nota de aprovação.

## Requisitos e evidências

| Seções | Situação atual e evidência |
|---|---|
| 1–3. HTTP/SQS, autorização e falhas | Caso de uso compartilhado; OIDC real, isolamento de provedor e endpoints internos; IAM testado; duplicatas, concorrência, pendências e recuperação exercitadas. |
| 4. Stack/Fx | Go, Fx, pgx, PostgreSQL, SQS, Keycloak, Compose e migrations presentes. Lifecycle real testado; startup exige migration 9 e há teste negativo com schema antigo. |
| 5. Invariantes | Dinheiro inteiro; idempotência SQL; saldo não negativo; ledger append-only; locks por carteira; atualização com versão; outbox após commit. Testes financeiros e de banco aprovados. |
| 6. Domínio | Money, Wallet, WagerTransaction e ledger encapsulados. Criação/reidratação separadas; erros financeiros classificáveis; Debit/Credit recebem Money e validam moeda. |
| 6.1. Money | Parsing e operações exatos, overflow, moedas e serialização inválida testados. JSON/Format retornam erro; FromMinor suporta valores internos negativos. Reconciliação usa subtração protegida contra overflow. |
| 6.2. Carteira | Identidade, jogador, saldo/moeda, versão e tempos; unicidade jogador/moeda; ledger e saldo atômicos; zero não incrementa versão. |
| 6.3. Transação | Agora carrega Money, chave/hash, referência interna, timestamps e resultado. Reidratação de pendências recupera metadados do banco. OPENING possui construtor interno e passa pelo domínio antes da persistência. Estados terminais protegidos. |
| 6.4–6.5. Ledger/inbox/outbox | UUIDs, equação, imutabilidade e unicidades; inbox e resultado durável no mesmo commit; eventos com snapshots imutáveis e retries. |
| 7. Operações | BET/WIN/LOSS/REFUND/ROLLBACK, referências compatíveis, código próprio para reversão sem saldo, proteção contra devolução duplicada, pendências com backoff e TTL. |
| 8. Concorrência | 50 duplicatas, disputa 100/80/80, carteiras independentes e três processos. Resultados financeiros/ledger verificados. |
| 9. API | Rotas, abertura zero/positiva, consultas, cursor opaco, hash canônico, replays/conflitos, reconciliação e health checks implementados e exercitados. |
| 10. Consumidor | FAILED segue à DLQ antes do delete. Teste real reenvia envelope, confirma duas entregas e um único efeito/evento. SIGTERM real em subprocesso após commit conclui mensagem ativa e deixa a próxima na fila. |
| 11. Publicador/eventos | Dois publishers ativos disputam outbox sob barreira; outra carteira progride. Testes de crash antes/depois do envio preservam eventId. Eventos têm tipos Go distintos e contratos financeiros tipados. |
| 12. Observabilidade | Logs JSON com correlação SQS; métricas de status, duplicatas HTTP/SQS, retries, DLQ, conflitos SQL, latência HTTP/SQS, lag e divergência. Lock timeout real verifica incremento do contador SQL. Tracing HTTP/SQS/outbox validado em coletor real; ativação opcional. |
| 13. Testes | Suíte completa final com -race aprovada após reconstrução das APIs, incluindo tracing ponta a ponta. Unitários de zero, moedas, erros, abertura, metadata e reidratação. Integração real inclui auth, constraints, inbox/outbox, crashes, SIGTERM, pendências e FAILED. |
| 14. Avaliação | Nenhuma violação eliminatória identificada nos cenários testados. Não equivale a certificação de ausência de defeitos ou aprovação do avaliador. Opcional não implementado: partidas dobradas. |
| 15. Entrega | Código, Compose, migrations, env de exemplo e documentação locais. Entrega pelo fork público maykonscheffer/backend-challenge-go. ZIP não faz parte da entrega. |

## Limites e verificações restantes

- Validação final concluída: proteção adicional de OPENING e teste específico passaram; go vet ./... passou. Logs: test-results/opening-final.txt e test-results/final-vet.txt.
- A versão anterior à inclusão do tracing passou em projeto Compose temporário com volumes novos, suíte completa com -race e testes IAM positivos/negativos. Integração: 21,455s. Log: test-results/delivery-clean.txt. Recursos temporários removidos; imagens/camadas existentes puderam ser reutilizadas. Publicação e reprodução a partir do GitHub entregue continuam pendentes.
- A cobertura de regras combina testes unitários e integração. Não se afirma uma matriz exaustiva de cada combinação inválida em ambas as camadas.
- O teste SIGTERM exercita o consumidor real em subprocesso; o teste Fx separado verifica início/encerramento e fechamento de pool. Não é teste de sinal em todos os containers simultaneamente com carga.
- Race instrumenta os processos de testes; os binários comuns das três APIs não são compilados com race.
- FIFO pode suprimir republicação imediata; não se aguardou a janela de cinco minutos. Eventos continuam exigindo deduplicação pelos consumidores externos.
- Produtor SQS é interno confiável. Perfis IAM compartilham volume local. MiniStack foi testado com parada/restauração graciosa, não perda de disco.
- Reidratação de OPENING agora também exige igualdade entre o resultado monetário e o valor original. Um snapshot inconsistente é rejeitado com ErrInvalid; teste específico e suíte limpa aprovados.

Reabrir código ou repetir testes somente diante de mudança, falha ou lacuna concreta. As evidências acima permanecem válidas para a versão testada; evitar refazer blocos já encerrados.


## Fechamento para entrega — 18/10/2026

Enunciado oficial conferido novamente contra os contratos, domínio, persistência,
constraints, workers, testes e instruções de execução. Nenhum requisito obrigatório
pendente foi identificado nesta conferência. Partidas dobradas foram explicitamente
excluídas da implementação por serem opcionais; benchmark, dashboard e tracing permanecem.
A validação mais recente do tracing e das regressões afetadas está em VALIDATION.md.
A conferência inicial não repetiu testes. Posteriormente, a revisão documental final executou a suíte completa e go vet com APIs reconstruídas, sem alterar regras financeiras.

Antes da entrega externa: conferir a seleção final do Git, criar o commit, publicar
o repositório e enviar seu link. A publicação depende de autorização do responsável.
Logs de execução em test-results são evidências locais, excluídas do repositório;
os comandos reproduzíveis e resultados resumidos estão na documentação versionada.