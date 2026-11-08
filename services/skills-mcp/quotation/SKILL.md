---
name: motor-quotation
description: Calculate, revise or compare a motor insurance quotation in a Chinese telephone conversation using the caller's confirmed plan.
---

# Motor insurance quotation

Use `quote_total` when the caller asks for a premium, changes coverage or wants to compare plans. For a general explanation of coverage, answer the question directly.

1. Use the host-provided call identifier and memory version. Pass the current working memory, including the last confirmed full selection under `memory.quote`. Use only information known for this call. An empty selection requests the CRM's default plan.
2. For a revision, pass the requested changes in `config`. For example, after “三者改成三百万，其他不动”, keep the previous selection in memory and pass `{"三者险":"300万"}`. An explicit removal uses `false`. For “两百万和三百万差多少”, pass one `configs` array with the two alternatives. A comparison alone does not change the caller's choice.
3. While pricing is pending, use the host's existing acknowledgement once and let the caller interrupt. If they change their selection, use the new memory version. Discard a result superseded by a newer turn or selection.
4. For `status: need`, ask the first outstanding question in Chinese. Use the returned choices. Record the answer in call memory and retry. Only skip a field when the CRM permits it.
5. For `status: quote`, read the returned currency, total and relevant coverage differences. Use each variant's own result and report any unavailable variant. Retain the priced selection and its version for the next change. A timeout or tool error has no confirmed premium.
