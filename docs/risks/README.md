# Risk-register conventions

The [risk register](risk-register.json) owns risk disposition and review state.
Threat models and ADRs own analysis and decisions; they link to this register
instead of copying lifecycle state into multiple documents.

Each risk has a stable `RSK-NNN` identity, one accountable owner, a concise
statement, severity, affected decision IDs, source Linear issues, control-story
IDs, review triggers, and exactly one lifecycle variant:

- `open` has no resolution metadata;
- `accepted` requires the accepting decision, acceptance date, and review date;
- `mitigated` requires verification evidence and date; and
- `closed` requires a closure date and rationale.

Do not express lifecycle with independent `open`, `accepted`, or `mitigated`
flags. A mitigation does not erase history: update the lifecycle and retain the
risk ID, statement, sources, controls, and review evidence.

Review the register when a listed trigger occurs, a linked decision is replaced,
a control test fails, a security incident occurs, or before the DS-200 gate.
Expired acceptances must return to `open` or be re-accepted by a current decision.
