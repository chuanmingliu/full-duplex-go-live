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
