# Benchmark HTTP reproduzível

Ferramenta separada em `cmd/benchmark`, sem alteração do domínio ou do processamento
financeiro. Não usa SQL direto e não requer serviços pagos. Use apenas o ambiente local
de demonstração: ela cria carteiras e operações persistidas, sem apagar históricos.

## Execução

Com Docker Desktop iniciado, na raiz da solução:

```powershell
docker compose up -d --build
docker compose --profile test run --rm --no-deps tests go run ./cmd/benchmark -requests 1000 -concurrency 8 -wallets 16 -output test-results/benchmark-many.json
docker compose --profile test run --rm --no-deps tests go run ./cmd/benchmark -requests 1000 -concurrency 8 -wallets 1 -output test-results/benchmark-shared.json
```

Execute depois que `/health/ready` estiver disponível nas três APIs. Não rode testes ou
outros benchmarks simultaneamente. Os JSONs ficam no host em `test-results`, excluído do
Git. O programa retorna código diferente de zero se houver falha HTTP, inconsistência,
divergência de identidade, contagem inesperada de replays ou falha de coleta das métricas.

Fora do container, os padrões são APIs em localhost:8080/8081/8082 e Keycloak em 8180.
`TEST_API_URLS` e `TEST_TOKEN_URL` substituem os destinos. Tokens são obtidos por
client_credentials e ficam apenas em memória; não entram nos relatórios. As variáveis
`BENCH_INTERNAL_SECRET` e `BENCH_PROVIDER_SECRET` substituem os segredos locais de exemplo.

## Metodologia

- Carga fechada: cada cliente aguarda sua resposta antes da próxima chamada; concorrência
  fixa e conexões HTTP reutilizadas. Não é uma taxa de chegada fixa nem capacidade máxima.
- Operações BET de 1.00 BRL; carteiras novas começam com 1000000.00 BRL. Dinheiro nunca é
  convertido em ponto flutuante; números decimais no relatório são durações e métricas.
- Por padrão, cada décima chamada repete a operação anterior com mesma chave e conteúdo:
  1000 chamadas representam 900 operações únicas e 100 replays. Não há retry automático.
- Distribuição entre todas as APIs. `-wallets 1` concentra locks numa carteira;
  `-wallets 16` permite trabalho independente. `-duplicate-every 0` desabilita reenvios.
- Autenticação, criação das carteiras, métricas iniciais e reconciliação ficam fora da
  janela de carga. Não há fase de aquecimento separada; pools/conexões podem aquecer nela.
- Throughput = chamadas concluídas / segundos da janela. p50/p95/p99 usam nearest-rank
  sobre todas as chamadas, incluindo falhas; status 0 representa falha de transporte.
- Contadores SQL/HTTP são somados entre instâncias e comparados antes/depois. Os gauges
  de outbox representam o mesmo banco e são coletados de uma única API, sem triplicar.
- Backlog e idade do evento mais antigo são amostrados antes/depois e aproximadamente
  a cada segundo. Máximos amostrados não capturam necessariamente picos entre amostras.
  São valores globais do ambiente, não exclusivos desta execução; a coleta adiciona carga.
- No fim, cada carteira é reconciliada: saldo esperado, soma do ledger, diferença zero
  e quantidade exata de lançamentos (incluindo abertura). Replays precisam preservar ID.
- Limites: até 100000 chamadas, 256 clientes e 1000 carteiras; timeout global de 5 minutos
  e 20 segundos por chamada. Comece com os parâmetros documentados.

## Ambiente e interpretação

O Compose executa três APIs, PostgreSQL 17.6, Keycloak 26.3.3 e MiniStack na mesma máquina.
O gerador usa a imagem Go 1.27.0 do serviço tests. O relatório registra Go, SO, arquitetura,
CPUs visíveis, GOMAXPROCS, quantidade de APIs, parâmetros e horário UTC. Registre também
a memória/CPUs atribuídas ao Docker ao comparar máquinas. Gerador, banco e APIs disputam
recursos locais; os números não estimam capacidade de produção e não há meta mínima de RPS.

Este benchmark exercita entrada HTTP e a publicação assíncrona da outbox. Não mede
throughput do consumidor de entrada SQS nem constitui teste de estabilidade prolongado.
Os testes funcionais de mensageria/recuperação permanecem separados.

## Execução observada — 18/10/2026

Docker Desktop com 28 CPUs visíveis e 16638906368 bytes de memória disponível ao engine
(aproximadamente 15,50 GiB). Go 1.27.0, linux/amd64, três APIs. Uma execução por cenário,
8 clientes, 1000 chamadas, 900 operações únicas e 100 replays em cada cenário.

| Carteiras | Duração | Chamadas/s | p50 ms | p95 ms | p99 ms | Erros HTTP/transporte | Conflitos SQL/HTTP |
|---|---:|---:|---:|---:|---:|---:|---:|
| 16 | 0,456 s | 2191,61 | 3,538 | 5,329 | 10,202 | 0 | 0 / 0 |
| 1 | 2,175 s | 459,71 | 18,622 | 26,366 | 28,776 | 0 | 0 / 0 |

Todas as carteiras reconciliaram e os replays mantiveram identidade. Sem retries SQL.
A ausência de erro de lock não significa ausência de espera: locks bem-sucedidos são
refletidos na latência, não no contador de erros de concorrência.

Outbox (global): no cenário com 16 carteiras, backlog foi de 32 para 1826, idade máxima
amostrada de 0s; com uma carteira, foi de 1822 para 3604, idade máxima de 3s. A segunda
execução herdou backlog da primeira. As chamadas terminaram antes da publicação de todos
os eventos; estes números não demonstram equilíbrio sustentado do publicador.

São amostras curtas para demonstrar o comando e a instrumentação, não um estudo estatístico
nem teste prolongado. O cenário de 16 carteiras durou menos que o intervalo de amostragem:
seus gauges refletem apenas os pontos inicial/final. Aumente -requests para estudos maiores.
Relatórios brutos locais: benchmark-many.json e benchmark-shared.json em test-results.
Smoke adicional: 100 chamadas com -race, quatro carteiras/clientes, zero falhas e integridade
aprovada. Go vet passou no código da ferramenta.
