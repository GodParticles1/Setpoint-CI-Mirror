import { validateWakeEnvelope } from "./openai-core.js";

// No env/context parameter: the deployed queue path has no binding capability.
// Keep the existing envelope contract, including the fixed repository boundary.
export default {
  async queue(batch) {
    for (const message of batch.messages) {
      const envelope = message.body;
      const valid = validateWakeEnvelope(envelope);
      const metadata = valid ? {
        delivery: envelope.delivery,
        repository: envelope.repository,
        event: envelope.event,
        signal: envelope.signal,
      } : {};
      console.log(JSON.stringify({
        type: "agent_observer_reserved_passive",
        mode: "RESERVED_PASSIVE",
        result: valid ? "PASSIVE_WAKE" : "INVALID_QUEUE_ENVELOPE",
        ...metadata,
        openai_dispatch: false,
        github_live_read: false,
        github_mutation_count: 0,
      }));
      // Reject and ACK invalid messages, preserving the poison-message policy.
      // Neither branch invokes active code or Registry storage.
      message.ack();
    }
  },
};
