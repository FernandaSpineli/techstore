// TechStore demo storefront. Plain JavaScript, no build step.
// Every value that comes from the API is inserted with textContent (via h),
// never as HTML.
"use strict";

const API = "/api/v1";
const money = new Intl.NumberFormat("pt-BR", { style: "currency", currency: "BRL" });
const dateTime = new Intl.DateTimeFormat("pt-BR", { dateStyle: "short", timeStyle: "short" });
const $main = document.getElementById("main");

// ---------------------------------------------------------------- helpers

// h builds an element: h("a", { href: "#/" }, "text", child, ...)
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v == null) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "class") el.className = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

function render(...nodes) {
  $main.replaceChildren(...nodes);
  $main.focus({ preventScroll: true });
  window.scrollTo(0, 0);
}

function notice(text, kind) {
  return h("p", { class: "notice" + (kind ? " " + kind : ""), role: kind === "error" ? "alert" : "status" }, text);
}

const statusLabel = {
  pending_payment: "Aguardando pagamento",
  paid: "Pago",
  shipped: "Enviado",
  delivered: "Entregue",
  cancelled: "Cancelado",
  expired: "Expirado",
};

// ---------------------------------------------------------------- session
// Tokens live in sessionStorage: they disappear with the tab. A production
// frontend would keep the refresh token in an HttpOnly cookie instead.

const session = {
  get access() { return sessionStorage.getItem("access"); },
  get refresh() { return sessionStorage.getItem("refresh"); },
  get user() { try { return JSON.parse(sessionStorage.getItem("user")); } catch { return null; } },
  save(tokens, user) {
    sessionStorage.setItem("access", tokens.access_token);
    sessionStorage.setItem("refresh", tokens.refresh_token);
    if (user) sessionStorage.setItem("user", JSON.stringify(user));
  },
  clear() { sessionStorage.clear(); },
};

// ---------------------------------------------------------------- API client

class APIError extends Error {
  constructor(status, body) {
    super(body?.error?.message || `HTTP ${status}`);
    this.status = status;
    this.code = body?.error?.code;
    this.details = body?.error?.details;
  }
}

async function api(method, path, { body, auth = false, headers = {}, retry = true } = {}) {
  const init = { method, headers: { ...headers } };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  if (auth && session.access) init.headers.Authorization = "Bearer " + session.access;

  const started = performance.now();
  const res = await fetch(API + path, init);
  trace(method, path, res, performance.now() - started);

  // One silent refresh when the access token has expired.
  if (res.status === 401 && auth && retry && session.refresh) {
    if (await refreshTokens()) return api(method, path, { body, auth, headers, retry: false });
  }
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new APIError(res.status, data);
  return { data, res };
}

async function refreshTokens() {
  try {
    const { data } = await api("POST", "/auth/refresh", { body: { refresh_token: session.refresh }, retry: false });
    session.save(data);
    return true;
  } catch {
    session.clear();
    renderAccount();
    return false;
  }
}

// ---------------------------------------------------------------- API trace

const $trace = document.getElementById("trace-list");

function trace(method, path, res, ms) {
  const code = res.status;
  const cls = code >= 500 ? "server" : code >= 400 ? "client" : "ok";
  const cache = res.headers.get("X-Cache");
  const li = h("li", { class: "new" },
    h("div", { class: "line1" },
      h("span", { class: "method" }, method),
      h("span", { class: "path" }, path),
      h("span", { class: "code " + cls }, code)),
    h("div", { class: "meta" },
      h("span", {}, `${Math.round(ms)} ms`),
      cache && h("span", { class: cache === "HIT" ? "hit" : "" }, "cache " + cache.toLowerCase()),
      res.headers.get("Idempotent-Replayed") && h("span", {}, "replay"),
      h("span", { title: "X-Request-ID" }, (res.headers.get("X-Request-ID") || "").slice(0, 12))));
  $trace.prepend(li);
  while ($trace.children.length > 40) $trace.lastChild.remove();
}

document.getElementById("trace-toggle").addEventListener("click", (e) => {
  const open = document.getElementById("trace").classList.toggle("open");
  e.currentTarget.setAttribute("aria-expanded", String(open));
  e.currentTarget.textContent = open ? "Fechar requisições" : "Ver requisições";
});

// ---------------------------------------------------------------- header

let cartCount = 0;

function renderAccount() {
  const nav = document.getElementById("account");
  const user = session.user;
  if (!user) {
    nav.replaceChildren(h("a", { href: "#/login" }, "Entrar"), h("a", { href: "#/register" }, "Criar conta"));
    return;
  }
  nav.replaceChildren(
    h("a", { href: "#/orders" }, "Pedidos"),
    h("a", { href: "#/cart" }, "Carrinho", cartCount > 0 && h("span", { class: "badge" }, cartCount)),
    h("a", { href: "#/", onclick: logout }, "Sair"));
}

async function refreshCartCount() {
  if (!session.access) { cartCount = 0; renderAccount(); return; }
  try {
    const { data } = await api("GET", "/cart", { auth: true });
    cartCount = data.item_count;
  } catch { cartCount = 0; }
  renderAccount();
}

async function logout(e) {
  e.preventDefault();
  if (session.refresh) await api("POST", "/auth/logout", { body: { refresh_token: session.refresh } }).catch(() => {});
  session.clear();
  cartCount = 0;
  renderAccount();
  location.hash = "#/";
}

document.getElementById("search-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const q = document.getElementById("q").value.trim();
  location.hash = q ? "#/?q=" + encodeURIComponent(q) : "#/";
});

function requireLogin() {
  if (session.access) return true;
  render(h("h1", {}, "Entre para continuar"),
    h("p", { class: "lede" }, "Carrinho e pedidos ficam na sua conta."),
    h("a", { class: "btn", href: "#/login" }, "Entrar"));
  return false;
}

// ---------------------------------------------------------------- catalog

async function catalogPage(params) {
  const q = params.get("q") || "";
  const category = params.get("category") || "";
  const sort = params.get("sort") || (q ? "relevance" : "newest");
  document.getElementById("q").value = q;

  const query = new URLSearchParams({ per_page: "50", sort });
  if (q) query.set("q", q);
  if (category) query.set("category", category);
  if (params.get("in_stock")) query.set("in_stock", "true");

  const [cats, list] = await Promise.all([api("GET", "/categories"), api("GET", "/products?" + query)]);

  const go = (changes) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(changes)) v ? next.set(k, v) : next.delete(k);
    location.hash = "#/?" + next;
  };

  const chips = h("div", { class: "chips", role: "group", "aria-label": "Categorias" },
    h("button", { class: "chip", type: "button", "aria-pressed": String(!category), onclick: () => go({ category: "" }) }, "Tudo"),
    cats.data.data.map((c) => h("button", {
      class: "chip", type: "button", "aria-pressed": String(category === c.slug), onclick: () => go({ category: c.slug }),
    }, c.name)));

  const sortSelect = h("select", { "aria-label": "Ordenar", onchange: (e) => go({ sort: e.target.value }) },
    [["newest", "Mais recentes"], ["price_asc", "Menor preço"], ["price_desc", "Maior preço"], ["name", "Nome"]]
      .concat(q ? [["relevance", "Relevância"]] : [])
      .map(([v, label]) => h("option", { value: v, selected: v === sort }, label)));

  const inStock = h("label", {}, h("input", {
    type: "checkbox", checked: !!params.get("in_stock"), onchange: (e) => go({ in_stock: e.target.checked ? "1" : "" }),
  }), " Só em estoque");

  const items = list.data.data;
  const table = items.length === 0
    ? h("div", { class: "empty" }, q ? `Nada encontrado para “${q}”. Tente outro termo.` : "Nenhum produto nesta categoria ainda.")
    : h("table", { class: "products" },
        h("thead", {}, h("tr", {}, h("th", {}, "Produto"), h("th", { class: "brand-col" }, "Marca"), h("th", { class: "num" }, "A partir de"), h("th", { class: "avail-col" }, "Disponibilidade"))),
        h("tbody", {}, items.map((p) => h("tr", {},
          h("td", { class: "name" }, h("a", { href: "#/p/" + p.slug }, p.name), h("div", { class: "stock out" }, p.category.name),
            h("div", { class: "stock-sm stock " + (p.in_stock ? "in" : "out") }, p.in_stock ? "Em estoque" : "Esgotado")),
          h("td", { class: "brand-col" }, p.brand),
          h("td", { class: "num" }, money.format(p.min_price_cents / 100)),
          h("td", { class: "avail-col" }, h("span", { class: "stock " + (p.in_stock ? "in" : "out") }, p.in_stock ? "Em estoque" : "Esgotado"))))));

  render(
    h("h1", {}, q ? `Resultados para “${q}”` : "Eletrônicos e acessórios"),
    h("p", { class: "lede" }, `${list.data.pagination.total} produto(s). Os preços estão em reais e o estoque é conferido de novo ao fechar o pedido.`),
    h("div", { class: "controls" }, chips, sortSelect, inStock),
    table);
}

async function productPage(slug) {
  const { data: p } = await api("GET", "/products/" + encodeURIComponent(slug));
  const firstAvailable = p.variants.find((v) => v.in_stock) || p.variants[0];
  const price = h("div", { class: "price" }, money.format(firstAvailable.price_cents / 100));
  const qty = h("input", { type: "number", min: "1", max: "99", value: "1", "aria-label": "Quantidade", class: "qty-input" });
  const msg = h("div");

  const variants = h("fieldset", { class: "variants" },
    h("legend", { class: "sr-only" }, "Versão"),
    p.variants.map((v) => h("label", { class: "variant" },
      h("input", {
        type: "radio", name: "variant", value: v.id, disabled: !v.in_stock, checked: v.id === firstAvailable.id,
        onchange: () => { price.textContent = money.format(v.price_cents / 100); },
      }),
      h("span", {}, h("strong", {}, v.name), h("span", { class: "stock block " + (v.in_stock ? "in" : "out") }, v.in_stock ? "Em estoque" : "Esgotado")),
      h("span", {}, money.format(v.price_cents / 100)))));

  const add = h("button", { class: "btn block", type: "button", disabled: !firstAvailable.in_stock }, "Adicionar ao carrinho");
  add.addEventListener("click", async () => {
    if (!session.access) { location.hash = "#/login"; return; }
    const variantId = variants.querySelector("input:checked")?.value;
    add.disabled = true;
    try {
      await api("POST", "/cart/items", { auth: true, body: { variant_id: variantId, quantity: Number(qty.value) } });
      msg.replaceChildren(notice("Adicionado ao carrinho.", "good"), h("a", { href: "#/cart" }, "Ver carrinho"));
      refreshCartCount();
    } catch (err) {
      const d = err.details;
      msg.replaceChildren(notice(err.code === "INSUFFICIENT_STOCK" && d ? `Só há ${d.available} unidade(s) disponível(is).` : err.message, "error"));
    } finally {
      add.disabled = false;
    }
  });

  render(
    h("p", {}, h("a", { href: "#/?category=" + p.category.slug }, p.category.name)),
    h("div", { class: "product" },
      h("div", {},
        h("h1", {}, p.name),
        h("p", { class: "lede" }, p.brand),
        p.description && h("p", { class: "description" }, p.description),
        h("h2", {}, "Escolha a versão"),
        variants),
      h("div", { class: "buy" }, price, h("p", {}, h("label", {}, "Quantidade ", qty)), add, msg)));
}

// ---------------------------------------------------------------- cart

async function cartPage(flash) {
  if (!requireLogin()) return;
  const { data: cart } = await api("GET", "/cart", { auth: true });
  cartCount = cart.item_count;
  renderAccount();

  if (cart.items.length === 0) {
    render(h("h1", {}, "Carrinho"), flash || "", h("div", { class: "empty" }, "Seu carrinho está vazio. ", h("a", { href: "#/" }, "Ver produtos")));
    return;
  }

  const change = async (variantId, quantity) => {
    try {
      if (quantity < 1) await api("DELETE", "/cart/items/" + variantId, { auth: true });
      else await api("PATCH", "/cart/items/" + variantId, { auth: true, body: { quantity } });
      cartPage();
    } catch (err) {
      cartPage(notice(err.code === "INSUFFICIENT_STOCK" ? `Só há ${err.details.available} unidade(s) disponível(is).` : err.message, "error"));
    }
  };

  const issueText = (it) => it.issue === "unavailable" ? "Não está mais à venda. Remova para continuar."
    : it.issue === "insufficient_stock" ? `Só há ${it.available_quantity} disponível(is). Ajuste a quantidade.` : null;

  const place = h("button", { class: "btn", type: "button", disabled: !cart.checkout_ready }, "Fechar pedido");
  place.addEventListener("click", async () => {
    place.disabled = true;
    try {
      // The key makes a double click or a retried request return the same
      // order instead of creating a second one.
      const { data: order } = await api("POST", "/orders", { auth: true, headers: { "Idempotency-Key": crypto.randomUUID() } });
      refreshCartCount();
      location.hash = "#/orders/" + order.id;
    } catch (err) {
      cartPage(notice(err.message, "error"));
    }
  });

  render(
    h("h1", {}, "Carrinho"),
    flash || "",
    h("table", { class: "lines" },
      h("thead", {}, h("tr", {}, h("th", {}, "Produto"), h("th", { class: "num hide-sm" }, "Preço"), h("th", {}, "Quantidade"), h("th", { class: "num" }, "Total"))),
      h("tbody", {}, cart.items.map((it) => h("tr", {},
        h("td", {}, h("a", { href: "#/p/" + it.product_slug }, it.product_name), " — ", it.variant_name, issueText(it) && h("span", { class: "issue" }, issueText(it))),
        h("td", { class: "num hide-sm" }, money.format(it.unit_price_cents / 100)),
        h("td", {}, h("span", { class: "qty" },
          h("button", { type: "button", "aria-label": "Diminuir", onclick: () => change(it.variant_id, it.quantity - 1) }, "−"),
          h("span", { "aria-label": "Quantidade" }, it.quantity),
          h("button", { type: "button", "aria-label": "Aumentar", onclick: () => change(it.variant_id, it.quantity + 1) }, "+"))),
        h("td", { class: "num" }, money.format(it.line_total_cents / 100)))))),
    h("div", { class: "summary" },
      h("span", { class: "total" }, "Subtotal ", money.format(cart.subtotal_cents / 100)),
      place),
    !cart.checkout_ready && h("p", { class: "lede" }, "Resolva os itens marcados para fechar o pedido."));
}

// ---------------------------------------------------------------- orders

async function ordersPage() {
  if (!requireLogin()) return;
  const { data } = await api("GET", "/orders?per_page=50", { auth: true });
  render(
    h("h1", {}, "Pedidos"),
    data.data.length === 0
      ? h("div", { class: "empty" }, "Você ainda não fez pedidos. ", h("a", { href: "#/" }, "Ver produtos"))
      : h("table", { class: "lines" },
          h("thead", {}, h("tr", {}, h("th", {}, "Pedido"), h("th", { class: "hide-sm" }, "Data"), h("th", {}, "Status"), h("th", { class: "num" }, "Total"))),
          h("tbody", {}, data.data.map((o) => h("tr", {},
            h("td", {}, h("a", { href: "#/orders/" + o.id }, "#" + o.id.slice(-8))),
            h("td", { class: "hide-sm" }, dateTime.format(new Date(o.created_at))),
            h("td", {}, h("span", { class: "status " + o.status }, statusLabel[o.status])),
            h("td", { class: "num" }, money.format(o.total_cents / 100)))))));
}

async function orderPage(id, flash) {
  if (!requireLogin()) return;
  const { data: o } = await api("GET", "/orders/" + id, { auth: true });

  const actions = h("div", { class: "summary" });
  if (o.status === "pending_payment") {
    const pay = h("button", { class: "btn", type: "button" }, "Pagar com Stripe");
    pay.addEventListener("click", async () => {
      pay.disabled = true;
      try {
        const { data } = await api("POST", `/orders/${o.id}/checkout`, { auth: true });
        location.href = data.checkout_url; // Stripe's hosted page
      } catch (err) {
        orderPage(id, notice(err.code === "PAYMENTS_UNAVAILABLE" ? "Pagamentos não estão configurados neste ambiente (defina as chaves do Stripe)." : err.message, "error"));
      }
    });
    const cancel = h("button", { class: "btn quiet", type: "button" }, "Cancelar pedido");
    cancel.addEventListener("click", async () => {
      try {
        await api("POST", `/orders/${o.id}/cancel`, { auth: true });
        orderPage(id, notice("Pedido cancelado e estoque devolvido.", "good"));
      } catch (err) {
        orderPage(id, notice(err.message, "error"));
      }
    });
    actions.append(h("span", {}, "Reservado até ", dateTime.format(new Date(o.expires_at))), h("span", {}, cancel, " ", pay));
  }

  render(
    h("p", {}, h("a", { href: "#/orders" }, "Pedidos")),
    h("h1", {}, "Pedido #" + o.id.slice(-8)),
    h("p", {}, h("span", { class: "status " + o.status }, statusLabel[o.status])),
    flash || "",
    h("table", { class: "lines" },
      h("thead", {}, h("tr", {}, h("th", {}, "Item"), h("th", { class: "num hide-sm" }, "Preço"), h("th", { class: "num" }, "Qtd."), h("th", { class: "num" }, "Total"))),
      h("tbody", {}, o.items.map((it) => h("tr", {},
        h("td", {}, it.product_name, " — ", it.variant_name),
        h("td", { class: "num hide-sm" }, money.format(it.unit_price_cents / 100)),
        h("td", { class: "num" }, it.quantity),
        h("td", { class: "num" }, money.format(it.line_total_cents / 100)))))),
    h("div", { class: "summary" }, h("span", { class: "total" }, "Total ", money.format(o.total_cents / 100))),
    actions,
    h("h2", {}, "Histórico"),
    h("ol", { class: "timeline" }, o.status_history.map((c) => h("li", {},
      h("strong", {}, statusLabel[c.to]), " — ", c.reason,
      h("time", { datetime: c.at }, dateTime.format(new Date(c.at)))))));
}

// After Stripe redirects back, the order is only marked paid once the signed
// webhook arrives, so this page polls instead of trusting the redirect.
async function checkoutSuccessPage(orderId) {
  render(h("h1", {}, "Confirmando pagamento"), h("p", { class: "lede" }, "O Stripe recebeu seu pagamento. Aguardando a confirmação assinada para liberar o pedido…"));
  for (let i = 0; i < 15; i++) {
    try {
      const { data } = await api("GET", "/orders/" + orderId, { auth: true });
      if (data.status !== "pending_payment") { location.replace("/#/orders/" + orderId); return; }
    } catch { break; }
    await new Promise((r) => setTimeout(r, 2000));
  }
  render(h("h1", {}, "Pagamento em processamento"),
    h("p", { class: "lede" }, "A confirmação ainda não chegou. Pagamentos por boleto podem levar até 3 dias úteis."),
    h("a", { class: "btn", href: "/#/orders/" + orderId }, "Ver pedido"));
}

// ---------------------------------------------------------------- auth pages

function formPage(title, lede, fields, submitLabel, onSubmit) {
  const msg = h("div");
  const form = h("form", { class: "form", novalidate: true },
    fields.map((f) => h("label", {}, f.label, f.hint && h("span", { class: "hint" }, f.hint),
      h("input", { name: f.name, type: f.type, autocomplete: f.autocomplete, required: true }))),
    h("button", { class: "btn", type: "submit" }, submitLabel));
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const values = Object.fromEntries(new FormData(form));
    try {
      await onSubmit(values, msg);
    } catch (err) {
      const problems = Array.isArray(err.details) ? err.details.map((d) => `${d.field}: ${d.message}`).join("; ") : "";
      msg.replaceChildren(notice(problems || err.message, "error"));
    }
  });
  render(h("h1", {}, title), lede && h("p", { class: "lede" }, lede), msg, form);
}

function signedIn(data) {
  session.save(data.tokens, data.user);
  refreshCartCount();
  location.hash = "#/";
}

function loginPage() {
  formPage("Entrar", null, [
    { label: "E-mail", name: "email", type: "email", autocomplete: "email" },
    { label: "Senha", name: "password", type: "password", autocomplete: "current-password" },
  ], "Entrar", async (v) => signedIn((await api("POST", "/auth/login", { body: v })).data));
  $main.append(h("p", {}, h("a", { href: "#/forgot" }, "Esqueci minha senha")));
}

function registerPage() {
  formPage("Criar conta", null, [
    { label: "Nome", name: "name", type: "text", autocomplete: "name" },
    { label: "E-mail", name: "email", type: "email", autocomplete: "email" },
    { label: "Senha", name: "password", type: "password", autocomplete: "new-password", hint: "Pelo menos 8 caracteres" },
  ], "Criar conta", async (v) => signedIn((await api("POST", "/auth/register", { body: v })).data));
}

function forgotPage() {
  formPage("Recuperar senha", "Enviaremos um link para criar uma nova senha.", [
    { label: "E-mail", name: "email", type: "email", autocomplete: "email" },
  ], "Enviar link", async (v, msg) => {
    const { data } = await api("POST", "/auth/password/forgot", { body: v });
    msg.replaceChildren(notice(data.message + " Em desenvolvimento, veja em http://localhost:8025.", "good"));
  });
}

function resetPage(token) {
  formPage("Nova senha", null, [
    { label: "Nova senha", name: "new_password", type: "password", autocomplete: "new-password", hint: "Pelo menos 8 caracteres" },
  ], "Salvar nova senha", async (v, msg) => {
    await api("POST", "/auth/password/reset", { body: { token, new_password: v.new_password } });
    msg.replaceChildren(notice("Senha alterada. Entre com a nova senha.", "good"), h("a", { href: "/#/login" }, "Entrar"));
  });
}

// ---------------------------------------------------------------- router

async function route() {
  const path = location.pathname;
  const params = new URLSearchParams(location.search);
  try {
    // Pages reached from outside: Stripe redirects and the reset email.
    if (path === "/checkout/success") return await checkoutSuccessPage(params.get("order_id"));
    if (path === "/checkout/cancel") { location.replace("/#/orders/" + encodeURIComponent(params.get("order_id") || "")); return; }
    if (path === "/reset-password") return resetPage(params.get("token") || "");

    const [hashPath, hashQuery] = location.hash.slice(1).split("?");
    const parts = (hashPath || "/").split("/").filter(Boolean);
    const hp = new URLSearchParams(hashQuery || "");
    switch (parts[0]) {
      case undefined: return await catalogPage(hp);
      case "p": return await productPage(parts[1]);
      case "cart": return await cartPage();
      case "orders": return parts[1] ? await orderPage(parts[1]) : await ordersPage();
      case "login": return loginPage();
      case "register": return registerPage();
      case "forgot": return forgotPage();
      default: render(h("h1", {}, "Página não encontrada"), h("a", { href: "#/" }, "Voltar à loja"));
    }
  } catch (err) {
    if (err.status === 404) render(h("h1", {}, "Não encontrado"), h("p", { class: "lede" }, err.message), h("a", { href: "#/" }, "Voltar à loja"));
    else render(h("h1", {}, "Algo deu errado"), notice(err.message, "error"));
  }
}

window.addEventListener("hashchange", route);
renderAccount();
refreshCartCount();
route();
