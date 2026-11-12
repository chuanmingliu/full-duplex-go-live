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
