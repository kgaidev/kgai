package viewtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"kgai/internal/event"
)

// The demo store: an online shop's checkout and billing as three people shaped it over
// seven months — sixty-odd decisions, vendors replaced, a cart moved between stores,
// two dead ends recorded, one conflict resolved and one still open. Screenshots and
// demos read it; a test keeps every event valid for the engine.

// demo builds the events. It tracks the heads per element so every decision supersedes
// what it replaces, the way kg ingest computes it, and chains hashes per install.
type demo struct {
	created map[string]bool
	heads   map[string][]string
	prev    map[string][]string // heads before the last decision, for concurrent ones
	shards  map[string][]event.Event
	order   []string
	clock   int64 // the Lamport clock every install shares once synced
	last    map[string]string
	IDs     map[string]string // decision id by title
}

// dec is one decision to record.
type dec struct {
	actor, at, title, why, author, refs string
	note                                bool // provenance only: a dead end or context, governs nothing
	concurrent                          bool // recorded without seeing the decision just before it
	muts                                []event.Mutation
}

func el(kind, name string) string { return event.ElementID(kind, name) }

// DemoElement is the id of a demo element, for tests.
func DemoElement(kind, name string) string { return el(kind, name) }

func up(kind, name string, props ...string) event.Mutation {
	m := event.Mutation{Op: event.MutUpsertElement, ElementID: el(kind, name), Kind: kind, Name: name}
	if len(props) > 0 {
		m.Props = map[string]string{}
		for i := 0; i+1 < len(props); i += 2 {
			m.Props[props[i]] = props[i+1]
		}
	}
	return m
}

func link(fromKind, from, kind, toKind, to string) event.Mutation {
	return event.Mutation{Op: event.MutAddLink, ElementID: el(fromKind, from), FromID: el(fromKind, from), ToID: el(toKind, to), LinkKind: kind}
}

func unlink(fromKind, from, kind, toKind, to string) event.Mutation {
	m := link(fromKind, from, kind, toKind, to)
	m.Op = event.MutRetireLink
	return m
}

func prop(kind, name, key, value string) event.Mutation {
	return event.Mutation{Op: event.MutSetProp, ElementID: el(kind, name), Key: key, Value: value}
}

func (b *demo) add(d dec) {
	install := "i-" + d.actor
	if !contains(b.order, install) {
		b.order = append(b.order, install)
	}
	// Replay orders events by Lamport time, so the clock must run across installs the
	// way a synced team's does: every event after the last one it could have seen. A
	// concurrent decision shares its predecessor's tick — neither saw the other.
	if !d.concurrent {
		b.clock++
	}
	var shapes, targets []string
	seen := map[string]bool{}
	governs := map[string]bool{}
	shape := func(id string) {
		if !seen[id] {
			seen[id] = true
			shapes = append(shapes, id)
		}
	}
	target := func(id string) {
		if !governs[id] {
			governs[id] = true
			targets = append(targets, id)
		}
	}
	exists := func(id string) {
		if !b.created[id] {
			panic(fmt.Sprintf("demo: %q refers to an element that does not exist yet", d.title))
		}
	}
	for _, m := range d.muts {
		switch m.Op {
		case event.MutUpsertElement:
			shape(m.ElementID)
			if !b.created[m.ElementID] {
				b.created[m.ElementID] = true
				target(m.ElementID)
			} else if len(m.Props) > 0 {
				target(m.ElementID)
			}
		case event.MutSetProp:
			exists(m.ElementID)
			shape(m.ElementID)
			target(m.ElementID)
		case event.MutAddLink, event.MutRetireLink:
			exists(m.FromID)
			exists(m.ToID)
			shape(m.FromID)
			shape(m.ToID)
			target(m.FromID)
		}
	}
	if d.note && len(targets) > 0 {
		panic(fmt.Sprintf("demo: note %q takes authority", d.title))
	}
	base := b.heads
	if d.concurrent {
		base = b.prev
	}
	var sup []string
	for _, t := range targets {
		for _, h := range base[t] {
			if !contains(sup, h) {
				sup = append(sup, h)
			}
		}
	}
	author := d.author
	if author == "" {
		author = d.actor
	}
	decision := event.Decision{Title: d.title, Rationale: d.why, Author: author, Refs: d.refs,
		Supersedes: sup, Shapes: shapes, Targets: targets, Mutations: d.muts, ProvenanceOnly: d.note}
	decision.ID = event.DecisionID(decision)
	b.prev = map[string][]string{}
	for k, v := range b.heads {
		b.prev[k] = append([]string(nil), v...)
	}
	for _, t := range targets {
		var hs []string
		for _, h := range b.heads[t] {
			if !contains(sup, h) {
				hs = append(hs, h)
			}
		}
		b.heads[t] = append(hs, decision.ID)
	}
	e := event.Event{Op: event.OpAssert, Actor: d.actor, InstallID: install, Lamport: b.clock, RecordedAt: d.at, Decision: &decision}
	if last := b.last[install]; last != "" {
		e.Parents = []string{last}
	}
	e.Hash = e.ComputeHash()
	b.last[install] = e.Hash
	b.shards[install] = append(b.shards[install], e)
	b.IDs[d.title] = decision.ID
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// BuildDemo records every demo decision and returns the builder with its shards.
func BuildDemo() *demo {
	b := &demo{created: map[string]bool{}, heads: map[string][]string{}, prev: map[string][]string{},
		shards: map[string][]event.Event{}, last: map[string]string{}, IDs: map[string]string{}}
	for _, d := range demoDecisions() {
		b.add(d)
	}
	return b
}

// Shards returns the events per install id, in the order the installs first recorded.
func (b *demo) Shards() (ids []string, byInstall map[string][]event.Event) {
	return b.order, b.shards
}

// WriteDemo writes the demo store at root: a config (alice's install), one shard per
// person, and the git files kg init writes — except that the config is committed here,
// since the demo ships its identity.
func WriteDemo(root string) error {
	b := BuildDemo()
	if err := os.MkdirAll(filepath.Join(root, "log"), 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"kg.config.json": `{"install_id":"i-alice","actor":"alice","schema_version":1}` + "\n",
		".gitignore":     "graph.kuzu*\n*.so\n.kg.lock\n",
		".gitattributes": "*.ndjson merge=union\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	for _, install := range b.order {
		f, err := os.Create(filepath.Join(root, "log", install+".ndjson"))
		if err != nil {
			return err
		}
		for _, e := range b.shards[install] {
			line, err := json.Marshal(e)
			if err != nil {
				f.Close()
				return err
			}
			if _, err := f.Write(append(line, '\n')); err != nil {
				f.Close()
				return err
			}
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

const (
	gh = "github:https://github.com/acme/shop/"
)

// demoDecisions is the story, in the order it was recorded.
func demoDecisions() []dec {
	return []dec{
		// ---- February: the shape of the shop --------------------------------------------
		{actor: "alice", at: "2026-02-03T09:12:00Z", title: "Checkout is its own domain; the Cart is part of it",
			why:  "Checkout owns everything from the cart to the confirmed order. Cart state is checkout state, not catalog state.",
			muts: []event.Mutation{up("feature", "Checkout", "paths", "src/checkout/*"), up("feature", "Cart", "paths", "src/checkout/cart/*"), link("feature", "Cart", "PART_OF", "feature", "Checkout")}},
		{actor: "alice", at: "2026-02-03T10:40:00Z", title: "The Web Storefront renders Checkout",
			why:  "One storefront, server-rendered; checkout is a flow inside it, not a separate app.",
			muts: []event.Mutation{up("component", "Web Storefront", "paths", "web/*"), link("component", "Web Storefront", "RENDERS", "feature", "Checkout")}},
		{actor: "alice", at: "2026-02-04T14:05:00Z", title: "Orders live in PostgreSQL",
			why:  "One relational source of truth for money-bearing records; everything else may be rebuilt from it.",
			muts: []event.Mutation{up("feature", "Order", "paths", "src/orders/*"), up("infra", "PostgreSQL"), link("feature", "Order", "STORES_IN", "infra", "PostgreSQL")}},
		{actor: "alice", at: "2026-02-05T11:30:00Z", title: "Invoice exists, part of Pricing",
			why:  "Invoices are priced items.",
			muts: []event.Mutation{up("feature", "Invoice", "paths", "src/billing/invoice/*"), up("feature", "Pricing", "paths", "src/billing/pricing/*"), link("feature", "Invoice", "PART_OF", "feature", "Pricing")}},
		{actor: "bob", at: "2026-02-10T09:50:00Z", title: "Payments go through Braintree",
			why:  "Braintree's vault covered our card-on-file need on day one, and the sandbox was up in an afternoon.",
			muts: []event.Mutation{up("service", "Payment Gateway", "paths", "src/payments/*"), up("vendor", "Braintree"), link("service", "Payment Gateway", "DEPENDS_ON", "vendor", "Braintree"), link("feature", "Checkout", "DEPENDS_ON", "service", "Payment Gateway")}},
		{actor: "bob", at: "2026-02-11T15:20:00Z", title: "Money is integer minor units with an explicit currency",
			why:  "Floating point rounding already cost a cent per line in the prototype. Every amount carries its currency; mixing currencies is a type error.",
			muts: []event.Mutation{up("concept", "Money"), up("concept", "Currency"), link("concept", "Money", "DEPENDS_ON", "concept", "Currency"), prop("concept", "Money", "representation", "integer minor units")}},
		{actor: "carol", at: "2026-02-17T13:00:00Z", title: "The Checkout Form is one component, owned by the storefront",
			why:  "Address, shipping and payment were three forms with three validation styles. One component, one state.",
			muts: []event.Mutation{up("component", "Checkout Form", "paths", "web/checkout/*"), link("component", "Checkout Form", "PART_OF", "component", "Web Storefront"), link("component", "Checkout Form", "RENDERS", "feature", "Checkout")}},
		{actor: "alice", at: "2026-02-24T10:10:00Z", title: "Product Catalog and Search are separate features",
			why:  "Search reads the catalog; it does not own products. Splitting them lets the index be rebuilt without touching catalog data.",
			muts: []event.Mutation{up("feature", "Product Catalog", "paths", "src/catalog/*"), up("feature", "Search", "paths", "src/search/*"), link("feature", "Search", "DEPENDS_ON", "feature", "Product Catalog")}},

		// ---- March: billing takes shape -------------------------------------------------
		{actor: "bob", at: "2026-03-02T09:00:00Z", title: "Tax is computed by a Tax Service, not inside Pricing",
			why:  "Tax rules change by jurisdiction and quarter; Pricing must not ship with them baked in.",
			muts: []event.Mutation{up("service", "Tax Service", "paths", "src/tax/*"), up("concept", "Tax Rate"), link("feature", "Pricing", "DEPENDS_ON", "service", "Tax Service"), link("service", "Tax Service", "DEPENDS_ON", "concept", "Tax Rate")}},
		{actor: "alice", at: "2026-03-04T16:45:00Z", title: "Invoice renders standalone, outside Pricing",
			why: "An invoice is a record of an order, not a priced item. It must not hang off Pricing.", refs: gh + "pull/61",
			muts: []event.Mutation{unlink("feature", "Invoice", "PART_OF", "feature", "Pricing"), link("feature", "Invoice", "PART_OF", "feature", "Order"), prop("feature", "Invoice", "display", "standalone")}},
		{actor: "carol", at: "2026-03-09T10:30:00Z", title: "Invoices render through one Invoice Renderer, HTML and PDF",
			why:  "The email preview and the download must be the same document; two renderers drifted within a week.",
			muts: []event.Mutation{up("component", "Invoice Renderer", "paths", "src/billing/invoice/render/*"), link("feature", "Invoice", "DEPENDS_ON", "component", "Invoice Renderer")}},
		{actor: "bob", at: "2026-03-09T11:15:00Z", title: "Considered PDF-only invoices, rejected", note: true,
			why:  "Accessibility and the email preview both need HTML; PDF stays a download.",
			muts: []event.Mutation{up("feature", "Invoice")}},
		{actor: "alice", at: "2026-03-16T09:40:00Z", title: "Discounts are part of Pricing",
			why:  "A discount is a pricing rule with a condition, nothing more.",
			muts: []event.Mutation{up("feature", "Discounts", "paths", "src/billing/pricing/discounts/*"), link("feature", "Discounts", "PART_OF", "feature", "Pricing")}},
		{actor: "bob", at: "2026-03-18T14:00:00Z", title: "A Price Calculator owns every price computation",
			why:  "Line totals were computed in four places with three rounding rules. One calculator, one rounding rule, tested once.",
			muts: []event.Mutation{up("component", "Price Calculator", "paths", "src/billing/pricing/calc.ts"), link("feature", "Pricing", "DEPENDS_ON", "component", "Price Calculator"), link("component", "Price Calculator", "DEPENDS_ON", "concept", "Money")}},
		{actor: "carol", at: "2026-03-23T12:20:00Z", title: "Customer Account is a feature of its own, behind the Auth Service",
			why:  "Accounts were a tab of Checkout; returning customers need them without a cart.",
			muts: []event.Mutation{up("feature", "Customer Account", "paths", "src/accounts/*"), up("service", "Auth Service", "paths", "src/auth/*"), link("feature", "Customer Account", "DEPENDS_ON", "service", "Auth Service")}},
		{actor: "alice", at: "2026-03-30T15:55:00Z", title: "Notifications go out through an Email Service on SendGrid",
			why:  "Order mail was sent from three places with three templates; one service, one provider, one log.",
			muts: []event.Mutation{up("feature", "Notifications", "paths", "src/notify/*"), up("service", "Email Service", "paths", "src/notify/email/*"), up("vendor", "SendGrid"), link("feature", "Notifications", "DEPENDS_ON", "service", "Email Service"), link("service", "Email Service", "DEPENDS_ON", "vendor", "SendGrid")}},

		// ---- April: orders, refunds, shipping -------------------------------------------
		{actor: "bob", at: "2026-04-06T09:30:00Z", title: "Every payment call carries an Idempotency Key",
			why:  "A retried charge must never become a second charge. The key is the order id plus the attempt.",
			muts: []event.Mutation{up("concept", "Idempotency Key"), link("service", "Payment Gateway", "DEPENDS_ON", "concept", "Idempotency Key"), prop("service", "Payment Gateway", "retries", "idempotent")}},
		{actor: "alice", at: "2026-04-08T10:00:00Z", title: "Orders move through an explicit Order State Machine",
			why:  "Status was a free-text column. Transitions are now code, and every state has a test.",
			muts: []event.Mutation{up("concept", "Order State Machine"), up("component", "Order Pipeline", "paths", "src/orders/pipeline/*"), link("feature", "Order", "DEPENDS_ON", "component", "Order Pipeline"), link("component", "Order Pipeline", "DEPENDS_ON", "concept", "Order State Machine"), prop("feature", "Order", "states", "created, paid, fulfilled, cancelled, refunded")}},
		{actor: "carol", at: "2026-04-13T11:45:00Z", title: "Order History is part of Customer Account",
			why:  "Customers look for their orders under their account, not under checkout.",
			muts: []event.Mutation{up("feature", "Order History", "paths", "web/account/orders/*"), link("feature", "Order History", "PART_OF", "feature", "Customer Account"), link("feature", "Order History", "DEPENDS_ON", "feature", "Order")}},
		{actor: "bob", at: "2026-04-15T14:30:00Z", title: "Refunds are a feature; the Refund Window is a policy",
			why: "How long a customer may return is a business rule that changes; the refund flow does not.", refs: gh + "issues/88",
			muts: []event.Mutation{up("feature", "Refunds", "paths", "src/refunds/*"), up("policy", "Refund Window"), link("feature", "Refunds", "GOVERNED_BY", "policy", "Refund Window"), link("feature", "Refunds", "DEPENDS_ON", "service", "Payment Gateway"), prop("policy", "Refund Window", "days", "14")}},
		{actor: "alice", at: "2026-04-20T09:15:00Z", title: "Inventory is a service the Cart asks before checkout",
			why:  "Overselling on launch day. The cart reserves stock; checkout confirms it.",
			muts: []event.Mutation{up("service", "Inventory Service", "paths", "src/inventory/*"), link("feature", "Cart", "DEPENDS_ON", "service", "Inventory Service")}},
		{actor: "bob", at: "2026-04-22T16:10:00Z", title: "Shipping is priced by a Shipping Service",
			why:  "Carrier rates and zones are data, not checkout code.",
			muts: []event.Mutation{up("service", "Shipping Service", "paths", "src/shipping/*"), link("feature", "Checkout", "DEPENDS_ON", "service", "Shipping Service")}},
		{actor: "carol", at: "2026-04-27T10:05:00Z", title: "Free shipping from 50",
			why:  "Marketing's number from the spring campaign brief.",
			muts: []event.Mutation{up("policy", "Free Shipping Threshold"), link("service", "Shipping Service", "GOVERNED_BY", "policy", "Free Shipping Threshold"), prop("policy", "Free Shipping Threshold", "amount", "50 EUR")}},
		{actor: "bob", at: "2026-04-27T10:20:00Z", title: "Free shipping from 75", concurrent: true,
			why:  "Finance's number: below 75 the margin on small orders goes negative with free shipping.",
			muts: []event.Mutation{up("policy", "Free Shipping Threshold"), prop("policy", "Free Shipping Threshold", "amount", "75 EUR")}},

		// ---- May: the conflict resolved, payments replaced, the cart moved --------------
		{actor: "alice", at: "2026-05-04T09:00:00Z", title: "Free shipping from 60 — one threshold, agreed with marketing and finance",
			why:  "Two thresholds were recorded on the same morning by two people; 60 is the compromise both signed off.",
			muts: []event.Mutation{prop("policy", "Free Shipping Threshold", "amount", "60 EUR")}},
		{actor: "bob", at: "2026-05-06T13:30:00Z", title: "Payments move from Braintree to Stripe",
			why: "Braintree's EU payout delays and missing SEPA support. Stripe covers both, and the card vault migrates through their import.", refs: gh + "pull/142",
			muts: []event.Mutation{up("vendor", "Stripe"), unlink("service", "Payment Gateway", "DEPENDS_ON", "vendor", "Braintree"), link("service", "Payment Gateway", "DEPENDS_ON", "vendor", "Stripe"), prop("service", "Payment Gateway", "provider", "stripe")}},
		{actor: "bob", at: "2026-05-06T15:00:00Z", title: "Stripe webhooks land in one Webhook Handler",
			why:  "Every payment event enters through one door, is verified once, and is applied idempotently.",
			muts: []event.Mutation{up("component", "Webhook Handler", "paths", "src/payments/webhooks/*"), link("component", "Webhook Handler", "PART_OF", "service", "Payment Gateway"), link("component", "Webhook Handler", "DEPENDS_ON", "concept", "Idempotency Key")}},
		{actor: "alice", at: "2026-05-11T10:20:00Z", title: "The Cart lives in Redis, not PostgreSQL",
			why:  "Carts are hot, short-lived and forgiving; their writes were 40 % of Postgres IOPS for data nobody keeps.",
			muts: []event.Mutation{up("infra", "Redis"), link("feature", "Cart", "STORES_IN", "infra", "Redis"), prop("feature", "Cart", "ttl", "30 days")}},
		{actor: "carol", at: "2026-05-13T11:00:00Z", title: "Wishlist is part of Customer Account",
			why:  "A wishlist without an account cannot follow the customer.",
			muts: []event.Mutation{up("feature", "Wishlist", "paths", "web/account/wishlist/*"), link("feature", "Wishlist", "PART_OF", "feature", "Customer Account")}},
		{actor: "alice", at: "2026-05-18T14:40:00Z", title: "The Payment Gateway is in PCI scope; nothing else is",
			why:  "Card data never touches the storefront: Stripe's elements post straight to Stripe, the gateway sees tokens.",
			muts: []event.Mutation{up("policy", "PCI Scope"), link("service", "Payment Gateway", "GOVERNED_BY", "policy", "PCI Scope"), prop("policy", "PCI Scope", "boundary", "payment gateway only; the storefront never sees card data")}},
		{actor: "bob", at: "2026-05-20T09:10:00Z", title: "Sessions are kept in Redis by the Auth Service",
			why:  "Sticky sessions blocked rolling deploys.",
			muts: []event.Mutation{link("service", "Auth Service", "STORES_IN", "infra", "Redis")}},
		{actor: "carol", at: "2026-05-25T16:30:00Z", title: "The Admin Console is a second front end",
			why:  "Support handles refunds and orders all day; the storefront's account pages are not the tool for that.",
			muts: []event.Mutation{up("component", "Admin Console", "paths", "admin/*"), link("component", "Admin Console", "RENDERS", "feature", "Refunds"), link("component", "Admin Console", "RENDERS", "feature", "Order")}},
		{actor: "alice", at: "2026-05-27T10:00:00Z", title: "Evaluated Algolia for search, too costly at our volume", note: true, author: "alice + Claude",
			why:  "Good relevance out of the box, but the record count would put us in the top tier within a year.",
			muts: []event.Mutation{up("feature", "Search")}},

		// ---- June: search, archives, policies -------------------------------------------
		{actor: "alice", at: "2026-06-01T09:30:00Z", title: "Search runs on Meilisearch through a Search Index service", author: "alice + Claude",
			why:  "Self-hosted, typo-tolerant, and the index is rebuilt from the catalog in minutes.",
			muts: []event.Mutation{up("service", "Search Index", "paths", "src/search/index/*"), up("vendor", "Meilisearch"), link("feature", "Search", "DEPENDS_ON", "service", "Search Index"), link("service", "Search Index", "DEPENDS_ON", "vendor", "Meilisearch")}},
		{actor: "bob", at: "2026-06-03T11:20:00Z", title: "Invoices are archived in S3 for ten years",
			why:  "Accounting law; the database keeps the current copy, the archive keeps every issued one.",
			muts: []event.Mutation{up("infra", "S3"), link("feature", "Invoice", "STORES_IN", "infra", "S3"), prop("feature", "Invoice", "archive", "s3, 10 years")}},
		{actor: "alice", at: "2026-06-08T14:00:00Z", title: "Customer data is kept three years after the last order",
			why:  "GDPR retention: long enough for warranty claims, no longer.",
			muts: []event.Mutation{up("policy", "Retention Policy"), link("feature", "Customer Account", "GOVERNED_BY", "policy", "Retention Policy"), prop("policy", "Retention Policy", "period", "3 years after the last order")}},
		{actor: "carol", at: "2026-06-10T10:45:00Z", title: "Reviews are a feature of the Product Catalog",
			why:  "A review belongs to a product, not to an order.",
			muts: []event.Mutation{up("feature", "Reviews", "paths", "src/reviews/*"), link("feature", "Reviews", "PART_OF", "feature", "Product Catalog")}},
		{actor: "bob", at: "2026-06-15T09:00:00Z", title: "A Rate Limiter guards the Payment Gateway and the Auth Service",
			why:  "Card testing attacks in May. Limits are per account and per IP, counted in Redis.",
			muts: []event.Mutation{up("component", "Rate Limiter", "paths", "src/infra/ratelimit/*"), link("service", "Payment Gateway", "DEPENDS_ON", "component", "Rate Limiter"), link("service", "Auth Service", "DEPENDS_ON", "component", "Rate Limiter"), link("component", "Rate Limiter", "DEPENDS_ON", "infra", "Redis")}},
		{actor: "alice", at: "2026-06-17T15:30:00Z", title: "Subscriptions are a feature built on Orders",
			why:  "A subscription is a promise to place orders; it must not be a second order model.",
			muts: []event.Mutation{up("feature", "Subscription", "paths", "src/subscriptions/*"), link("feature", "Subscription", "DEPENDS_ON", "feature", "Order"), link("feature", "Subscription", "DEPENDS_ON", "service", "Payment Gateway")}},
		{actor: "bob", at: "2026-06-22T10:10:00Z", title: "Order states gain 'partially refunded'",
			why:  "Support refunds one line of a three-line order daily; the state machine had no word for it.",
			muts: []event.Mutation{prop("feature", "Order", "states", "created, paid, fulfilled, cancelled, partially refunded, refunded")}},
		{actor: "carol", at: "2026-06-24T13:15:00Z", title: "Email templates embed the rendered invoice",
			why:  "The invoice mail showed a summary that disagreed with the attached PDF once too often.",
			muts: []event.Mutation{up("component", "Email Templates", "paths", "src/notify/templates/*"), link("component", "Email Templates", "PART_OF", "feature", "Notifications"), link("component", "Email Templates", "DEPENDS_ON", "component", "Invoice Renderer")}},
		{actor: "alice", at: "2026-06-29T09:50:00Z", title: "Discount codes are validated by the Price Calculator, not the Checkout Form",
			why:  "Client-side validation leaked the code list in the bundle.",
			muts: []event.Mutation{link("feature", "Discounts", "DEPENDS_ON", "component", "Price Calculator"), prop("feature", "Discounts", "validation", "server-side, in the price calculator")}},

		// ---- July: details that stick ---------------------------------------------------
		{actor: "bob", at: "2026-07-02T09:00:00Z", title: "Tax rates are fetched daily and cached in Redis",
			why:  "The rate provider's API is rate-limited and slow; a day-old rate is what the law expects anyway.",
			muts: []event.Mutation{link("service", "Tax Service", "STORES_IN", "infra", "Redis"), prop("service", "Tax Service", "rates", "daily fetch, redis cache")}},
		{actor: "carol", at: "2026-07-06T11:30:00Z", title: "The Checkout Form is four steps: address, shipping, payment, review",
			why:  "One long form lost customers at the payment field; steps with a progress bar convert better in the test.",
			muts: []event.Mutation{prop("component", "Checkout Form", "steps", "address, shipping, payment, review")}},
		{actor: "alice", at: "2026-07-08T14:20:00Z", title: "Orders are exported nightly to S3 for analytics",
			why:  "Analytics ran against the production database and locked tables at 9 am.",
			muts: []event.Mutation{up("component", "Order Export", "paths", "src/orders/export/*"), link("component", "Order Export", "PART_OF", "feature", "Order"), link("component", "Order Export", "DEPENDS_ON", "infra", "S3")}},
		{actor: "bob", at: "2026-07-13T10:00:00Z", title: "SEPA Direct Debit and Apple Pay are payment methods",
			why:  "Both were the reason for moving to Stripe; both go live together.",
			muts: []event.Mutation{prop("service", "Payment Gateway", "methods", "card, sepa direct debit, apple pay")}},
		{actor: "alice", at: "2026-07-15T09:45:00Z", title: "The Search Index is rebuilt from the Catalog, never patched", author: "alice + Claude",
			why:  "Incremental updates drifted from the catalog twice; a full hourly rebuild takes four minutes.",
			muts: []event.Mutation{prop("service", "Search Index", "rebuild", "full, from the catalog, hourly")}},
		{actor: "carol", at: "2026-07-20T15:00:00Z", title: "Order History paginates by month",
			why:  "Infinite scroll over years of orders never found anything.",
			muts: []event.Mutation{prop("feature", "Order History", "paging", "by month")}},
		{actor: "bob", at: "2026-07-22T11:10:00Z", title: "Refunds go back to the original payment method only",
			why:  "Refunds to another card are the fraud pattern the gateway flagged; the policy is now code.",
			muts: []event.Mutation{prop("feature", "Refunds", "method", "original payment method only")}},
		{actor: "alice", at: "2026-07-27T10:30:00Z", title: "Inventory reservations expire with the cart",
			why:  "Stock held by abandoned carts is stock nobody can buy.",
			muts: []event.Mutation{prop("service", "Inventory Service", "reservation", "held while the cart lives, 30 days")}},
		{actor: "carol", at: "2026-07-29T14:00:00Z", title: "Wishlist items move to the cart in one step",
			why:  "The most requested account feature in support tickets this quarter.",
			muts: []event.Mutation{link("feature", "Wishlist", "DEPENDS_ON", "feature", "Cart")}},

		// ---- August: the open question --------------------------------------------------
		{actor: "bob", at: "2026-08-03T09:20:00Z", title: "Apple Pay goes through Stripe's Payment Request API",
			why:  "One integration for Apple Pay and Google Pay; the storefront never sees a wallet token.",
			muts: []event.Mutation{prop("service", "Payment Gateway", "wallets", "stripe payment request api")}},
		{actor: "alice", at: "2026-08-05T11:00:00Z", title: "Subscriptions renew through the Order Pipeline",
			why:  "A renewal is an order like any other; the pipeline already knows how to charge, fulfil and notify.",
			muts: []event.Mutation{link("feature", "Subscription", "DEPENDS_ON", "component", "Order Pipeline"), prop("feature", "Subscription", "renewal", "creates an order through the pipeline")}},
		{actor: "carol", at: "2026-08-10T10:15:00Z", title: "The Admin Console renders Subscriptions",
			why:  "Support pauses and resumes subscriptions daily.",
			muts: []event.Mutation{link("component", "Admin Console", "RENDERS", "feature", "Subscription")}},
		{actor: "bob", at: "2026-08-12T16:00:00Z", title: "The Webhook Handler replays from Stripe after a gap",
			why:  "A four-hour outage in July lost eleven payment events; the event list API replays them.",
			muts: []event.Mutation{prop("component", "Webhook Handler", "recovery", "replay from stripe's event list")}},
		{actor: "alice", at: "2026-08-17T09:30:00Z", title: "Considered moving Orders to an event store, rejected for now", note: true,
			why:  "PostgreSQL with the state machine is enough at this volume; revisit at ten times the orders.",
			muts: []event.Mutation{up("feature", "Order")}},
		{actor: "alice", at: "2026-08-19T10:00:00Z", title: "Refund Window: 14 days, counted from delivery",
			why: "Legal minimum in the EU, counted from the day the parcel arrives.", refs: gh + "issues/301",
			muts: []event.Mutation{prop("policy", "Refund Window", "days", "14, from delivery")}},
		{actor: "bob", at: "2026-08-19T10:05:00Z", title: "Refund Window: 30 days, counted from payment", concurrent: true,
			why: "Marketing promised 30 days in the summer campaign; counting from payment keeps the accounting simple.", refs: gh + "issues/301",
			muts: []event.Mutation{prop("policy", "Refund Window", "days", "30, from payment")}},
		{actor: "carol", at: "2026-08-24T13:40:00Z", title: "Reviews require a fulfilled order",
			why:  "Verified purchases only; it ended the review spam in a week.",
			muts: []event.Mutation{link("feature", "Reviews", "DEPENDS_ON", "feature", "Order"), prop("feature", "Reviews", "eligibility", "fulfilled order")}},
		{actor: "alice", at: "2026-08-26T09:00:00Z", title: "The Email Service moves from SendGrid to Postmark",
			why:  "SendGrid's shared IP reputation kept invoices in spam; Postmark's transactional-only pool does not.",
			muts: []event.Mutation{up("vendor", "Postmark"), unlink("service", "Email Service", "DEPENDS_ON", "vendor", "SendGrid"), link("service", "Email Service", "DEPENDS_ON", "vendor", "Postmark")}},
		{actor: "bob", at: "2026-08-27T15:30:00Z", title: "Every outgoing webhook is signed",
			why:  "Partners asked how to trust our calls; HMAC over the body with a per-partner secret.",
			muts: []event.Mutation{up("component", "Outbound Webhooks", "paths", "src/webhooks/out/*"), link("component", "Outbound Webhooks", "PART_OF", "feature", "Notifications"), prop("component", "Outbound Webhooks", "signing", "hmac sha256, per-partner secret")}},
	}
}
