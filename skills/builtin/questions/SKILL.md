---
name: questions
description: Formulate the minimum load-bearing questions needed to unblock a decision or workflow stage.
metadata:
  version: "1.0.0"
  capability: "darkstar:questions"
---

# Questions

Read the supplied context first and do not ask for information already present. Ask only questions whose answers can change scope, acceptance, architecture, permissions, risk treatment, or route selection.

Group closely related uncertainty and keep the set small. Explain the decision each question unlocks, give concise mutually exclusive choices when the option space is known, and make any safe default explicit. Do not disguise a recommendation as a question or require the user to restate evidence DARKSTAR already holds.

Return structured questions with an identifier, prompt, reason, affected decision, whether the answer is required, and any proposed default. If no load-bearing uncertainty remains, return an empty question set and say the stage can continue.
