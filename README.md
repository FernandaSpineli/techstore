# TechStore

[![CI](https://github.com/FernandaSpineli/techstore/actions/workflows/ci.yml/badge.svg)](https://github.com/FernandaSpineli/techstore/actions/workflows/ci.yml)

Backend de e-commerce de eletrônicos e acessórios, escrito em Go. Cobre
contas de usuário, catálogo com busca, variações e estoque, carrinho, pedidos
com reserva de estoque, pagamento via Stripe Checkout com webhooks assinados
e uma vitrine de demonstração embutida que mostra cada chamada feita à API.

**Stack:** Go 1.27 (`net/http` e `log/slog` da biblioteca padrão) ·
PostgreSQL 18 (pgx, SQL escrito à mão, migrations com goose) · Redis 8 ·
Stripe · Docker · Testcontainers · GitHub Actions

## Destaques

- **Sem venda acima do estoque, mesmo com acessos simultâneos.** O estoque é
  reservado dentro da transação do pedido, com as linhas travadas sempre na
  mesma ordem (evitando deadlock) e constraints `CHECK` como última barreira.
  Os testes colocam 8 clientes disputando 3 unidades e 16 pedidos
  concorrentes com variações em comum.
- **Pagamentos que não podem ser forjados nem aplicados duas vezes.** Só um
  webhook assinado pelo Stripe marca um pedido como pago. Cada evento é
  deduplicado na mesma transação que aplica seu efeito, o valor é conferido,
  e pagamentos assíncronos (boleto), atrasados ou em duplicidade são tratados.
  Veja o [ADR 003](docs/decisions/003-payments.md).
- **Redis apenas onde se justifica:** rate limit compartilhado entre
  instâncias, cache do catálogo com versionamento e `Idempotency-Key` na
  criação de pedidos. Se o Redis cair, tudo continua funcionando (*fail
  open*). Veja o [ADR 002](docs/decisions/002-redis.md).
- **Autenticação feita com cuidado:** bcrypt, JWT com algoritmo fixado,
  refresh tokens rotativos com detecção de reuso e recuperação de senha que
  não revela quais e-mails estão cadastrados.
- **Logs consultáveis e seguros.** JSON estruturado com IDs de requisição e
  de usuário, mais de 60 eventos de negócio nomeados e um teste que percorre
  os logs de uma compra completa procurando segredos vazados.
- **Testes contra infraestrutura real:** 133 testes (233 contando subtestes)
  e 89% de cobertura de código, rodando sobre PostgreSQL, Redis e Mailpit em
  containers, além de um servidor falso da API do Stripe.

## Como rodar

Requer Docker com Compose v2.

```bash
cp .env.example .env        # depois defina JWT_SECRET (openssl rand -base64 48)
make up                     # postgres, redis, mailpit, migrations e API
make seed                   # carrega um catálogo de exemplo
open http://localhost:8080  # vitrine de demonstração
```

Para usar as rotas de administração, crie uma conta (pela vitrine ou com
`POST /api/v1/auth/register`) e promova-a a admin:

```bash
make admin EMAIL=voce@exemplo.com
```

Os e-mails de recuperação de senha chegam no Mailpit, em
<http://localhost:8025>.

Se as portas 8080, 5432, 6379 ou 8025 já estiverem em uso, altere
`HTTP_PORT`, `POSTGRES_PORT`, `REDIS_PORT` ou `MAILPIT_UI_PORT` no `.env`.

### Pagamentos (Stripe em modo de teste)

Pagamentos são opcionais no ambiente local. Sem as chaves, o checkout
responde `503 PAYMENTS_UNAVAILABLE` e todo o resto funciona normalmente.

1. Coloque uma chave secreta de **teste** no `.env`:
   `STRIPE_SECRET_KEY=sk_test_...`. Chaves de produção são recusadas, a menos
   que `APP_ENV=production`.
2. Suba o Stripe CLI, que encaminha os webhooks para a API:
   ```bash
   docker compose --profile stripe up -d
   docker compose logs stripe   # "Your webhook signing secret is whsec_..."
   ```
3. Copie o valor `whsec_...` para `STRIPE_WEBHOOK_SECRET` no `.env` e
   reinicie a API: `docker compose up -d api`.
4. Na vitrine, faça um pedido e clique em **Pagar com Stripe**. Use o cartão
   `4242 4242 4242 4242`, qualquer data futura e qualquer CVC. O pedido passa
   para `paid` assim que o webhook assinado chega.

Os testes automatizados nunca chamam o Stripe: usam um gateway falso e
webhooks assinados localmente com o mesmo código de verificação da produção.

## Configuração

Toda a configuração vem de variáveis de ambiente. Se algum valor for
inválido, a aplicação não sobe e lista todos os problemas de uma vez. Veja o
[.env.example](.env.example).

| Variável | Padrão | Função |
| --- | --- | --- |
| `APP_ENV` | `development` | `development`, `test` ou `production` |
| `HTTP_ADDR` | `:8080` | Endereço em que a API escuta |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `DATABASE_URL` | — (obrigatória) | Conexão com o PostgreSQL |
| `REDIS_URL` | — (obrigatória) | Conexão com o Redis |
| `JWT_SECRET` | — (obrigatória, ≥ 32 caracteres) | Assina os access tokens |
| `ACCESS_TOKEN_TTL` / `REFRESH_TOKEN_TTL` | `15m` / `720h` | Validade dos tokens |
| `PASSWORD_RESET_TTL` | `30m` | Validade do link de recuperação de senha |
| `ORDER_RESERVATION_TTL` | `30m` | Por quanto tempo um pedido não pago segura o estoque |
| `APP_BASE_URL` | `http://localhost:8080` | Usada nos e-mails e nos redirecionamentos do Stripe |
| `RATE_LIMIT_AUTH_PER_MINUTE` / `RATE_LIMIT_API_PER_MINUTE` | `10` / `300` | Limites por IP |
| `CATALOG_CACHE_TTL` | `60s` | Tempo máximo que o cache do catálogo pode ficar desatualizado |
| `CORS_ALLOWED_ORIGINS` | vazio (desligado) | Origens separadas por vírgula, para frontends externos |
| `STRIPE_SECRET_KEY` / `STRIPE_WEBHOOK_SECRET` | vazio (pagamentos desligados) | Obrigatórias em produção |
| `SMTP_ADDR`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `MAIL_FROM` | Mailpit | Envio de e-mails |
| `SHUTDOWN_TIMEOUT` | `15s` | Tempo máximo para o desligamento gracioso |

## API

Todas as rotas ficam sob `/api/v1`. O contrato completo está em
[docs/openapi.yaml](docs/openapi.yaml) (OpenAPI 3.1, validado por linter e
comparado com as rotas reais por um teste).

| Área | Rotas |
| --- | --- |
| Autenticação | `POST /auth/register` · `/auth/login` · `/auth/refresh` · `/auth/logout` · `/auth/password/forgot` · `/auth/password/reset` |
| Perfil | `GET`/`PATCH /users/me` · `PUT /users/me/password` |
| Catálogo | `GET /categories` · `GET /products?q=&category=&brand=&min_price=&max_price=&in_stock=&sort=&page=&per_page=` · `GET /products/{slug}` |
| Carrinho | `GET`/`DELETE /cart` · `POST /cart/items` · `PATCH`/`DELETE /cart/items/{variantId}` |
| Pedidos | `POST /orders` (`Idempotency-Key`) · `GET /orders` · `GET /orders/{id}` · `POST /orders/{id}/cancel` |
| Pagamentos | `POST /orders/{id}/checkout` · `POST /payments/webhook` |
| Admin | categorias, produtos, variações, estoque (`/variants/{id}/inventory`), `/admin/products`, `/admin/orders`, `PATCH /admin/orders/{id}/status` |

Todos os erros seguem o mesmo formato:

```json
{
  "error": {
    "code": "INSUFFICIENT_STOCK",
    "message": "Not enough units in stock for some items",
    "details": [{ "variant_id": "…", "requested": 4, "available": 3 }],
    "request_id": "9b69a44c5000acbf81baf8dedce4f07a"
  }
}
```

Os status HTTP respeitam seu significado: 400 corpo malformado, 401 sem
autenticação, 403 sem permissão, 404 não encontrado (inclusive pedidos de
outros usuários), 409 conflito com o estado atual, 422 campos inválidos, 429
limite de requisições excedido e 502/503 para falhas no provedor de
pagamento. Erros internos nunca expõem detalhes do servidor.

```bash
# Navegar pelo catálogo
curl 'localhost:8080/api/v1/products?q=iph&sort=price_asc'
# Entrar e fechar o pedido com o que está no carrinho
TOKEN=$(curl -s -XPOST localhost:8080/api/v1/auth/login \
  -d '{"email":"voce@exemplo.com","password":"…"}' | jq -r .tokens.access_token)
curl -XPOST localhost:8080/api/v1/orders -H "Authorization: Bearer $TOKEN" \
  -H "Idempotency-Key: $(uuidgen)"
```

## Banco de dados

```mermaid
erDiagram
    users ||--o{ refresh_tokens : possui
    users ||--o{ password_reset_tokens : possui
    users ||--o| carts : possui
    carts ||--o{ cart_items : contem
    categories ||--o{ products : agrupa
    products ||--o{ product_variants : "vendido como"
    product_variants ||--o| inventory : "tem estoque"
    product_variants ||--o{ cart_items : ""
    users ||--o{ orders : faz
    orders ||--o{ order_items : "registra copia"
    orders ||--o{ order_status_history : audita
    orders ||--o{ payments : "pago por"
    product_variants ||--o{ order_items : ""
```

As migrations ficam em [migrations/](migrations) e são embutidas no binário
(`api migrate up|down|status`; o compose as executa antes de subir a API).
Chaves UUIDv7, valores monetários em centavos (inteiros) e regras de negócio
garantidas no próprio banco com `CHECK`, índices únicos e índices únicos
parciais. Os itens do pedido guardam uma cópia de nome e preço no momento da
compra, então alterações no catálogo não mudam pedidos antigos. Veja o
[ADR 001](docs/decisions/001-database.md).

## Desenvolvimento

| Comando | O que faz |
| --- | --- |
| `make test` | Todos os testes com o race detector (precisa de Docker para o Testcontainers) |
| `make test-unit` | Só os testes que não dependem de containers |
| `make cover` | Relatório de cobertura em HTML |
| `make lint` / `make fmt` | golangci-lint v2 / gofumpt + goimports, com versões fixas e rodando em Docker |
| `make build` | Binário estático em `bin/` |
| `make run` | Roda a API na máquina, usando os bancos do compose |
| `make up` / `make down` / `make logs` | Gerencia a stack do compose |

### Testes

- **Unitários:** as regras de negócio isoladas (máquina de estados do pedido,
  totais do carrinho, validação de tokens, montagem da busca, configuração).
- **Integração:** migrations para cima e para baixo, constraints do schema,
  recursos do Redis, envio de e-mail pelo Mailpit e o cliente do Stripe
  contra uma API falsa.
- **API:** o roteador real, com toda a montagem e os middlewares, sobre
  PostgreSQL e Redis. Cobrem a matriz de autorização (401/403 em todas as
  rotas protegidas), os cenários de falha (estoque insuficiente, quantidade
  inválida, produto inexistente, pagamento recusado, webhooks forjados e
  duplicados) e as condições de corrida.

Cada teste recebe o próprio banco, clonado de um template já migrado, o que
mantém os testes isolados e rápidos (cerca de 15 s para a suíte completa num
notebook, depois de baixadas as imagens).

### CI

O [`.github/workflows/ci.yml`](.github/workflows/ci.yml) roda a cada push e
pull request:

1. **lint:** `go mod tidy -diff` e golangci-lint.
2. **test:** `go vet` e a suíte completa com `-race`, com resumo e artefato de
   cobertura.
3. **security:** govulncheck, gitleaks em todo o histórico e hadolint.
4. **build:** o binário e a imagem Docker (com cache do BuildKit).

O Dependabot mantém atualizados os módulos Go, as actions e as imagens base.

## Documentação

A documentação técnica detalhada está em inglês:

- [Arquitetura](docs/architecture.md): módulos, ciclo de vida da requisição,
  máquina de estados do pedido, fluxo do checkout, estratégia de
  concorrência, eventos de log e segurança.
- Decisões de arquitetura (ADRs):
  [001 banco de dados](docs/decisions/001-database.md) ·
  [002 Redis](docs/decisions/002-redis.md) ·
  [003 pagamentos](docs/decisions/003-payments.md)
- [Especificação OpenAPI](docs/openapi.yaml)

## Limitações conhecidas

- Reembolsos são manuais: pagamentos atrasados ou duplicados são sinalizados
  nos logs para tratamento.
- Um pedido não pode ser cancelado enquanto a sessão do Stripe estiver aberta
  (cerca de 30 minutos).
- O rate limit usa o IP da conexão; atrás de um proxy reverso, seria preciso
  ler o header encaminhado pelo proxy.
- Não é possível alterar o e-mail da conta.

Mais detalhes em [architecture.md](docs/architecture.md#known-limitations).
