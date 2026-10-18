# Dashboard local opcional

Painel separado em `cmd/dashboard`. Não altera a API, o schema ou as regras financeiras
e não exige Grafana/Prometheus, CDN, pacotes JavaScript ou planos pagos.

Com o Compose do projeto em execução, abra outro terminal na raiz da solução:

```powershell
go run ./cmd/dashboard
```

Acesse **http://127.0.0.1:8090**. Encerre com Ctrl+C. Go deve estar no PATH.
O servidor fica restrito ao loopback, sem porta adicional no Compose. A autenticação
client_credentials acontece no processo Go; o navegador nunca recebe o token do Keycloak
nem o segredo. Não compartilhe esse servidor por túnel ou proxy público.

São exibidos estados persistidos, backlog/idade da outbox, taxa HTTP, médias de latência
HTTP/SQS, duplicatas, conflitos SQL, retries, transferências à DLQ e divergências.
O gráfico guarda as últimas 60 leituras na memória do navegador. Não há banco de séries
temporais, alertas, exportação de histórico ou percentis: p95/p99 estão no benchmark.

Atualização a cada 5 segundos, com botão de pausa e aviso de coleta indisponível.
Gauges do PostgreSQL são lidos uma vez; contadores são somados entre as três instâncias.
Taxas e latências são calculadas por diferenças entre leituras, portanto não aparecem
na primeira coleta. Reinício de processos pode zerar contadores e afetar intervalos.
O monitor adiciona requisições a /metrics; pause-o antes de comparar benchmarks.

Destinos padrão: localhost:8080/8081/8082; Keycloak em localhost:8180.
`TEST_API_URLS`, `TEST_TOKEN_URL` e `DASHBOARD_INTERNAL_SECRET` permitem configuração
local alternativa. Apenas métricas são intermediadas; não há proxy de rotas financeiras.
Respostas não são armazenadas em cache; Host/Origin são verificados para impedir que
outros sites usem o proxy local autenticado. O acesso local em si não tem login adicional.

Validação focada, sem repetir a suíte financeira:

```powershell
docker compose --profile test run --rm --no-deps tests go test -race ./cmd/dashboard
docker compose --profile test run --rm --no-deps tests go vet ./cmd/dashboard ./cmd/benchmark
```

Os testes verificam a página, restrição de Host/Origin, consulta autenticada às três APIs
reais, cache do token e ausência do token/segredo na resposta ao navegador.
