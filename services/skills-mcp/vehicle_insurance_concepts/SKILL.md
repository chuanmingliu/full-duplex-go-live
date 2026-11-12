---
name: vehicle_insurance_concepts
description: 解释中国大陆车险的保障对象、险种区别、保额、保费、免赔和理赔基本概念，适用于交强险、商业三者险、车损险和车上人员责任险的知识咨询。
---

根据来电人当前问题选用下面的知识，补充到原始外呼提示的回答中。先用一两句自然口语回答关键点，需要时再给一个简短例子。保留原始提示中的身份、语气和对话进度，避免重新开场或无关推销。

客户要求“全部档位”“有哪些”“还有其他吗”时，完整性优先于上述简短示例规则。以本轮工具说明中的活动报价方案为唯一选项来源，完整列出所问险种的已配置档位，可分短句讲述，允许客户打断。不要用行业常见档位代替配置，不要让客户去报价页面自己看。未提供相应险种或档位时，明确说明当前配置未提供，无法确认可选项，不能声称市场上没有此产品。只咨询选项不代表同意投保、修改方案或计算报价。保障限制、除外责任或调休规则未在活动目录或所提供的条款注明时，要说无法确认，不要用常识补出限制。

“医保外用药”可能指三者、司机或乘客的附加险。先说明活动方案中提供哪些分类；客户未明确范围时，确认对应主险后列出该分类全部档位。明确列出配置中的“共享保额”选项并解释它与独立保额的区别，不要混用三类档位。本技能不保存另一份固定档位表，以免与 CRM 配置失同步。

## 保障对象

- 交强险是法定的机动车交通事故责任强制保险。车辆发生道路交通事故，造成受害人的人身伤亡或财产损失时，在法定分项限额内赔偿；受害人范围排除本车人员和被保险人。解释时分别说明人身和财产这两类保障，再说明自己车辆维修、本车人员受伤需看对应保障。责任认定、适用限额和法定情形影响具体赔偿。
- 商业第三者责任险按合同承担对第三者的赔偿责任，通常衔接交强险分项限额以上的部分。说明其与交强险的层次关系，结合合同约定的责任范围、限额和免赔条款解释。
- 车损险针对被保险车辆自身的损失，在所投保合同约定的事故、自然灾害等责任范围内赔付。具体除外责任、免赔额和车辆类型需看保单。
- 车上人员责任险针对事故中本车驾驶人、乘客的人身伤亡责任，按投保座位、每座限额等合同约定处理。

## 常见概念

- 保费是为获得保险保障支付的费用；保额或责任限额是对应保障的赔偿额度上限。赔偿仍取决于实际损失、责任和合同。
- 免赔额是合同约定由被保险人自行承担的损失金额。解释某份保单时，以该保单条款为依据。
- 涉及个人保单是否承保、何时生效或某次事故能赔多少时，说明需要核对保单和事故情况。优先回答已知的基本原理，缺少关键事实时只追问相关信息。

## 与业务操作配合

知识咨询仅解释问题。实际计算或调整报价时交由报价技能及 quote_total 的权威结果处理，保留用户已经确认的保障选项。多个技能同时命中时，先回应概念疑问，再执行用户明确请求的业务动作。用户拒绝报价而继续问保障时，继续解释保障。

对“交强险一般保障什么？”的回答应覆盖交通事故、第三方人身伤亡和财产损失、责任限额，以及本车人员和被保险人的范围边界。使用当轮自然表述，避免机械照读本段。

## 维护依据

以下资料供维护知识时核验，通话中按用户需要解释。具体数字、费率和产品变动需查当前适用条款或权威业务数据。

- [机动车交通事故责任强制保险条例，第三条、第二十一至二十三条](https://www.nfra.gov.cn/cn/view/pages/ItemDetail.html?docId=879939)
- [机动车商业保险示范条款及险种责任范围](https://static2.4008000000.com/help/toubao/chexian/tiaokuan.shtml)

## Configured full motor catalog

For this deployment, `motor-full` separates driver and passenger coverages and
their riders. The plan's tiers are product configuration, not a statement about
all insurers. Refer quotation requests to `motor-quotation` / `quote_total`.

A rider's `parent` in the CRM plan identifies the coverage it accompanies.
Keep `精神损害险`, `医保外用药责任险`, and `绝对免赔率` associated with the
specified 三者/司机/乘客 scope. Do not collapse them into a generic rider.
`共享保额` refers to the configured parent limit; it does not add a second
independent limit. A percentage choice is a deductible setting, not a currency
amount or a promised premium discount. The configured holiday rider is a
separate selection; its applicability remains subject to the actual policy.
Vehicle-tax collection is shown separately from insurance premium.

Only explain contractual eligibility/exclusions when supported by the supplied
policy terms. Never infer an insurer's exact compensation or price from the
local quotation simulator's synthetic rates.
