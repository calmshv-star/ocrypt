import { describe, expect, it } from "vitest";
import { expectedPolicyHead, sourceForClass } from "./RetentionControlPage";

describe("retention control admission helpers", () => {
  it("bootstraps the first policy with zero version and fence", () => {
    expect(expectedPolicyHead()).toEqual({ expectedVersion: 0, expectedFence: 0 });
    expect(expectedPolicyHead({ version: 4, head_fence: 7 })).toEqual({ expectedVersion: 4, expectedFence: 7 });
  });

  it("binds every record hold to its only admitted source table", () => {
    expect(sourceForClass).toEqual({
      callback_event_body: "callback_events",
      event_history_payload: "event_history",
      published_outbox_payload: "outbox_events",
    });
  });
});
