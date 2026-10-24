# Notification setup recipes

These recipes use existing portal notification channels. Saving a recipe never
sends a message. Use an explicit test, inspect its delivery record, then enable a
routing rule. Keep recovery and signup mail on the separate authentication path.

## Email

1. Open Manage > Email and save your site's SMTP transport. A blank password keeps
   the saved password. Test this transport with an address you control.
2. Open Manage > Notifications > Channels and create an Email channel.
3. For a shared operations mailbox, add comma-separated fixed recipients and use
   a destination rule. That mailbox is external egress controlled by the admin.
4. For each account's own mailbox, leave fixed recipients empty and choose an
   account/OU grant or task-creator rule. Each account needs an Email address.
5. Select registered events and explicit recipients. An empty grant list selects
   nobody. A grant never grants access to the underlying report or task.
6. Send one explicit test and check the delivery history. Accepted means the SMTP
   relay accepted the message; it is not a read receipt.

## Slack

1. Obtain an Incoming Webhook URL for the intended Slack destination using your
   workspace's app configuration. Verify the target channel in Slack.
2. Create a Slack channel in the portal and paste the HTTPS URL. The portal allows
   the approved Slack host and never returns the secret path after saving.
3. Use a fixed destination rule. Slack membership is managed in Slack, not by the
   portal's per-account inbox grants. Do not use an OU rule to imply Slack privacy.
4. Send one test. Inspect both Slack and the portal delivery history.
5. A blank URL during editing keeps the stored one. Explicit clearing requires
   disabling the destination. Ordinary settings saves never send messages.

Slack's payload adapter produces its text object and escapes mention syntax.
Do not put a Slack robot URL into the raw Webhook adapter: raw event JSON is a
different contract. Official setup reference:
https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/

## Raw signed Webhook

1. Configure a receiver that accepts an HTTP POST with JSON bytes.
2. Create a raw Webhook channel with its HTTP(S) URL and a random signing secret.
   Use HTTPS outside an independently protected internal network.
3. Verify `X-Report-Portal-Signature` as `sha256=<hex HMAC-SHA256>` over the exact
   received bytes. Parse the body only after verification. Never reserialize it
   to compute the signature: JSON whitespace and large integers matter.
4. Add a destination rule for the registered events the receiver understands.
5. Explicit tests have event header `notification.test`; they do not pretend a
   report or a paid task was created. Test requests contain synthetic data only.
6. Return 2xx only after the receiver has accepted responsibility. Durable storage
   plus asynchronous local processing is preferable to holding the response open.
   Rejected requests should return a definite non-2xx result.
7. Store an idempotency/business key before downstream side effects. The portal
   can retry a definite rejection, and the network can lose an acceptance reply.

Additional event/delivery ID headers help correlate attempts. The legacy HMAC
covers the body, not those headers: they are not authenticated event identity.
Do not authorize actions solely from the event-name or ID headers. Use verified
body fields and your receiver's durable idempotency policy. A retained verified
body hash can conservatively reject replay of identical signed content. Distinct
legitimate occurrences can have identical bodies, so choose and document your
business deduplication semantics before production side effects.

## n8n signed receiver example

Download `n8n-signed-receiver.json` from this directory and import it into a
self-hosted n8n instance. It ships inactive, with no credentials, pinned data,
outgoing integrations or paid actions. Its purpose is a verifiable starting
point: accept exact signed bytes or reject them, then display a body digest.
It is not a production durable queue or a complete business workflow.

1. Provision a dedicated receiver signing secret of at least 16 characters in
   the n8n runtime as `REPORT_PORTAL_WEBHOOK_SECRET`; use the same value in the
   portal raw channel's signing-secret input. Do not paste it into the workflow
   JSON or share an exported execution containing it.
2. Allow the `crypto` built-in module in this trusted Code-node runtime with
   `NODE_FUNCTION_ALLOW_BUILTIN=crypto`. If using task runners, configure that
   runtime as described in the official module guide. Do not enable all modules.
3. The example reads its secret through `$env`. The deployment must permit this
   access for trusted workflow code. If your environment blocks it, use your
   approved secret/credential mechanism instead; missing access fails closed.
   Do not weaken a shared untrusted workflow environment just to run a demo.
4. In the Webhook node, keep POST, Raw Body enabled and response mode set to the
   Respond to Webhook node. The Code node retrieves the binary field `data`
   through `getBinaryDataBuffer`, which also supports n8n binary storage modes.
5. Listen on the n8n Test URL, configure it in a temporary portal raw channel,
   and run the portal's channel test. A valid request returns a verified-only
   200 response; a bad/missing signature takes the 401 branch. No side effect
   occurs. Changing the body without recomputing its signature must be rejected.
6. Inspect `body_utf8` only after the verification node. It preserves the exact
   body, including IDs beyond JavaScript's safe integer range. A downstream
   ordinary `JSON.parse` would lose those integers; use a lossless parser or
   preserve them as strings according to your consumer's schema.
7. Before adding any real action, persist the verified business/deduplication
   key in durable storage with a unique constraint, and arrange retry of work
   accepted there. Do not use workflow static memory as a concurrent inbox.
8. Publish the completed workflow and replace the temporary destination with
   the Production URL. Test again. Deactivate or remove the temporary channel.
   A test URL registered only while listening is not a permanent destination.

The JSON structure and verification code are fixture-tested in this repository.
No live n8n instance is bundled or exercised by the repository test suite. Confirm
import and runtime settings on your installed n8n release before activation.

Official documentation checked for this recipe:

- Webhook raw-body and test/production behavior:
  https://docs.n8n.io/integrations/builtin/core-nodes/n8n-nodes-base.webhook/
- Binary-buffer access in a Code node:
  https://docs.n8n.io/code/cookbook/code-node/get-binary-data-buffer/
- Code-node module allow-list and task runners:
  https://docs.n8n.io/hosting/configuration/configuration-examples/modules-in-code-node/
- Runtime environment access:
  https://docs.n8n.io/hosting/configuration/environment-variables/security/
