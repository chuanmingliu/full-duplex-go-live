---
name: motor-quotation
description: Calculate, revise or compare a motor insurance quotation in a Chinese telephone conversation using the caller's confirmed plan.
---

# Motor insurance quotation

Use `quote_total` when the caller asks for a premium, changes coverage or wants to compare plans. For a general explanation of coverage, answer the question directly.

1. Use the host-provided call identifier and memory version. Pass the current working memory, including the last confirmed full selection under `memory.quote`. Use only information known for this call. An empty selection requests the CRM's default plan.
2. For a confirmed revision, pass only the requested changes in `config`. For example, after “三者改成三百万，其他不动”, pass `{"三者险":"300万"}`. The Active Call engine records the requested change before submitting the calculation, preserving the other coverages. An explicit removal uses `false`. For a hypothetical inquiry, use `configs` even for a single option. For “两百万和三百万差多少”, pass one `configs` array with the two alternatives. Keep alternatives separate from the caller's selected plan.
3. While pricing is pending, use the host's existing acknowledgement once and let the caller interrupt. If they change their selection, use the new memory version. Discard a result superseded by a newer turn or selection.
4. For `status: need`, ask the first outstanding question in Chinese. Use the returned choices. Record the answer in call memory and retry. Only skip a field when the CRM permits it.
5. For `status: quote`, read the returned currency, total and relevant coverage differences. Use each variant's own result and report any unavailable variant. Retain the priced selection and its version for the next change. A timeout or tool error has no confirmed premium.

## Full motor coverage selections

The active CRM plan defines the available coverages and tiers. When `motor-full`
is selected, use canonical `车损险`, `划痕险`, `三者险`, `司机责任险`,
`乘客责任险`, plus the individually named riders. Keep `精神损害险-三者`,
`精神损害险-司机`, and `精神损害险-乘客` separate, likewise all
`绝对免赔率-*` and `医保外用药责任险-*` selections. Do not merge driver and
passenger insurance into `座位险`.

Use explicit units: `300万`, `2万`, `5%`, or `共享保额`. A deductible percentage
is not an insured amount. `车船税: "代缴"` requests collection; `false` declines
collection. `假日三者翻倍险` is boolean; do not double the main liability tier
in memory. The backend prices the holiday rider separately.

Each rider requires its parent coverage. If the caller removes a parent while
keeping riders, clarify whether to cancel those riders or retain the parent.
Never silently add a main coverage or omit an unpriced rider. A `need` result
with `kind: dependency` asks for this decision. An unmapped or incomplete backend
result is a failed quotation, not a confirmed smaller premium. Comparison
alternatives obey the same dependency and tier rules and remain independent.

客户要各档单项保费及总价时，用一次 configs 比较所有指定的有效档位；逐档说清该险种保费与整单合计，不得把总价差额当成单项保费。单项保费只取各 variants[].lines 中对应 coverage 的 premium；缺失时说明未返回并查询。医保费可能是医保外用药的识别结果，结合最近明确的三者/司机/乘客范围确认；范围不清先问清，不能自动选择三者。共享保额是额度规则，本身不能判断是否收费。费用未知时回答‘是否收费、收多少，以本次报价明细为准’，既不能说免费，也不能断言一定收费或必须额外收费；只有权威报价明细明确返回该项保费为0时才能说本次报价为0元，不能承诺所有客户免费。没有客户统计数据时，不说大多数客户、大家一般都选某档。可以按客户需求解释取舍，不编造人群偏好。原始话术提及的驾乘险等产品若未在本轮活动目录中，不得报价、承诺可加保或主动推销；应说明当前方案未提供，并区分已配置的司机和乘客责任险。

客户泛指某一险种不同档位或全部档位的价格、没有限定比较子集时，若工具声明 compare_coverage，使用它指定险种，让引擎按活动目录完整展开（包括最低档），不要自行漏选。工具未声明该参数时，按活动目录逐项构造 configs。客户明确只比较两档等子集时，严格按指定档位使用 configs。比较不代表确认修改方案。

## Confirming the scope of a follow-up quotation

When the host exposes `revision_action` and `revision_id`, use its pending-request
contract for every follow-up pricing request, including hypothetical comparisons.
The original first quotation follows the normal flow. A comparison authorizes
pricing alternatives; it does not select one or change the confirmed policy.

1. Identify whether the caller wants a selected-policy change (`config`) or a
   comparison (`configs`, or `compare_coverage` for all configured tiers).
2. Briefly restate the requested scope and ask whether any other coverage needs
   adjustment. Speak naturally and invoke the staging tool in that same response. Do not
   finish with only a question; the pending request must be saved.
   An `awaiting_confirmation` result means pricing has not started. If the
   question has already been spoken this turn, do not ask it a second time.
3. Save each additional change with another staging `quote_total` call before
   describing it as part of the pending scope. A spoken acknowledgment does not
   update the pending request. For example, with 300万 already pending, “我还想
   去掉车损险” requires `{"config":{"车损险":false}}`. Use the returned pending
   scope for the confirmation question. For comparisons, send the complete updated
   `configs` including all shared changes in every relevant alternative. Use
   `config` only when the caller is choosing a revised policy. Do not silently
   turn exploration into a purchase or policy-selection decision.
4. If the caller has not specified an option, ask a targeted question first.
   For “车损再调整一下”, use the active catalog to clarify inclusion/removal or
   an explicitly supported rider. Do not invent an adjustable insured-value
   menu or ask for vehicle purchase information as a substitute for clarifying
   what the caller wants to change.
5. Interpret the answer against the preceding question. “有” after “还有别的
   保障要调整吗” means more changes. “没有了，就这样算” settles the scope.
   A bare affirmative to an unrelated question, silence or a clarification
   request does not authorize pricing.
6. Only after a later caller utterance clearly approves the complete scope,
   invoke `quote_total` with `revision_action: confirm` and the current
   `revision_id`. Omit `config`, `configs` and `compare_coverage` on that call.
   Use `revision_action: cancel` when the caller abandons the pending request.
   Never use `update_memory.quote` to bypass this contract.
7. If interrupted, answer the new question using the current pending scope.
   Preserve accumulated changes, and return to confirmation when appropriate.
   Respect a request to pause, cancel or end the call.
8. Once the tool says pricing has started, give at most one brief contextual
   acknowledgment when needed. A ready result can be presented immediately.
   Read only authoritative returned premiums and keep calculation time separate
   from conversational responsiveness.

### Natural wording

These examples describe intent and are not fixed playback templates. Vary the
phrasing and sentence structure across rounds while preserving the scope.
Check the last confirmation question in the conversation and use a different
question form; changing only the coverage amount is insufficient.

- Comparing tiers — “先看两百万、三百万和四百万这几个档，其他保障还需要一起调整吗？”
- Changing a policy — “三者提高到三百万，其他项目先保留，您还有别的想改的吗？”
- Adding a change — “这几个方案都去掉车损。还有哪一项需要一起看看？”
- After approval — “好，我按您确认的这几个方案核算一下。”

Do not say the premium is being calculated before approval. Do not mechanically
repeat “保费正在计算中”. For a request with unresolved choices, ask about those
choices instead of using a generic waiting message. When this host contract is
absent, retain its original tool parameters and configured acknowledgment flow.

Keep a confirmation question short. Name the actual changes, summarize the rest
as “其他保障保持不变”, and ask one question. Avoid rereading every unchanged
coverage. Stage the pending revision in that same response. If the caller adds
another change, vary the question while accurately preserving earlier changes.

## Interrupted changes and direct address

Speak directly to the caller as 您. Give the answer itself. Keep analysis such as
“客户要的是”, “用户想要”, and “我先说明一下” out of speech. For an unsupported
limit, say “您想选五百万，目前最高支持四百万。” and clarify their choice.

An interrupted answer can leave a coverage request unprocessed. Inspect the host's
unresolved request list alongside the pending revision. A spoken promise does not
prove the request was saved. Resolve “最高” using the active coverage catalog.
Before requesting confirmation, merge each clear change through `quote_total`
with `config` or `configs`, and put its exact `request_id` in
`resolved_request_ids`. Each submitted variant must explicitly include the
request’s `required_coverages`; updating the main coverage alone cannot resolve a
rider request. Preserve other pending changes. The host keeps unresolved
requests across interruption and history trimming and blocks confirmation until
they are reconciled. Ask about ambiguous changes. Use cancellation only when the
caller actually cancels the pending adjustments.

Keep confirmation concise. Until confirmation has been accepted, describe the
changes and ask about further adjustments without saying pricing has started.
