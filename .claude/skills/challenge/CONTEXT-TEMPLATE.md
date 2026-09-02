# CONTEXT.md TEMPLATE

## Structure

```md
# {Context Name}
{One or two sentence description of what this context is and why it exists.}

## Language
**Order**:
A confirmed request from a Customer for one or more items at agreed prices.
_Avoid_: Purchase, transaction

**Invoice**:
A request for payment sent to a customer after delivery.
_Avoid_: Bill, payment request

**Customer**:
A person or organization that places orders.
_Avoid_: Client, buyer, account

## Flagged ambiguities
**"Account"** — used for both the billing relationship (the **Customer**) and the login identity (the **User**).
Resolution: reserve "account" for neither; say **Customer** or **User** explicitly.

## Example dialogue
> **Dev:** When a Customer cancels, does the Invoice get voided?
> **Domain expert:** No — once an Order ships we've raised an Invoice, so cancellation becomes a Refund against that Invoice.
> **Dev:** So an Order can be cancelled before shipping, but after shipping it's a Refund, never a cancellation?
> **Domain expert:** Exactly. Different concepts.
```

## Rules
- **Be opinionated.** When multiple words exist for the same concept, pick the best one and list the others as aliases to avoid.
- **Flag conflicts explicitly.** If a term is used ambiguously, call it out in "Flagged ambiguities" with a clear resolution.
- **Keep definitions tight.** One or two sentences max. Define what it IS, not what it does.
- **Show relationships.** Use bold term names and express cardinality where obvious.
- **Only include terms specific to this project's context.** General programming concepts (timeouts, error types, utility patterns) don't belong even
  if the project uses them extensively. Before adding a term, ask: is this a concept unique to this context, or a general programming concept?
  Only the former belongs.
- **Group terms under subheadings** when natural clusters emerge. If all terms belong to a single cohesive area, a flat list is fine.
- **Write an example dialogue.** A conversation between a dev and a domain expert that demonstrates how the terms interact naturally and
  clarifies boundaries between related concepts.

## Single vs multi-context repos
**Single context:** One `CONTEXT.md` at the repo root.

**Multiple contexts:** A `CONTEXT-MAP.md` at the repo root lists the contexts, where they live, and how they relate to each other:

```md
# Context Map

## Contexts
- [Ordering](./src/ordering/CONTEXT.md) — receives and tracks customer orders
- [Billing](./src/billing/CONTEXT.md) — generates invoices and processes payments
- [Fulfillment](./src/fulfillment/CONTEXT.md) — manages warehouse picking and shipping

## Relationships
- **Ordering → Fulfillment**: Ordering emits `OrderPlaced` events; Fulfillment consumes them to start picking
- **Fulfillment → Billing**: Fulfillment emits `ShipmentDispatched` events; Billing consumes them to generate invoices
- **Ordering ↔ Billing**: Shared types for `CustomerId` and `Money`
```

The skill infers which structure applies:
- If `CONTEXT-MAP.md` exists, read it to find contexts
- If only a root `CONTEXT.md` exists, single context
- If neither exists, create a root `CONTEXT.md` lazily when the first term is resolved

When multiple contexts exist, infer which one the current topic relates to. If unclear, ask.