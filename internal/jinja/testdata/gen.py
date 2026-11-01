#!/usr/bin/env python3
"""Writes cases.json: templates, variables, and what Jinja2 itself renders.

    pip install jinja2 && python3 gen.py

The Go tests read cases.json and must produce the same text, or fail where
Jinja2 fails. Run this again after adding a case; the file it writes is the
reference, so it is committed.
"""
import itertools, json, os, random
import jinja2

cases = []

def add(template, **variables):
    cases.append((template, variables))

# --- printing values ---
for v in [None, True, False, 0, 7, -3, 1.0, 1.5, 0.1 + 0.2, 1e16, 1e15, 1e-5, 0.0001, 123456789.125,
          "", "文字", "it's", 'say "hi"', [1, "a", None, 2.0], [], {"a": 1, "b": "x"}, [[1], {"k": [True]}]]:
    add("[{{ v }}]", v=v)
    add("[{{ [v, v] }}]", v=v)
add("[{{ missing }}][{{ missing.part is defined }}]")
add("{{ (1, 2) }}{{ (1,) }}{{ () }}{{ ('a', 'b') }}")
add("{{ 'a' 'b' }}{{ \"q\\\"q\" }}{{ 'new\\nline' }}{{ 1_000 }}{{ 2.5e3 }}")

# --- whitespace control and the end of the template ---
add("a \n {%- if x %} yes {%- else -%} no {% endif -%} \n b", x=True)
add("a \n {%- if x %} yes {%- else -%} no {% endif -%} \n b", x=False)
add("a  {{- x -}}  b  {{ x }}  c\n", x="X")
add("line\n")
add("line\n\n")
add("line\r\n")
add("{# gone #}a{#- gone -#}  b  {#- gone #} c")
add("{% raw %}{{ not }} {% touched %}{% endraw %}!")
add("a {%- raw %}  {{ x }}  {%- endraw -%}   b")
add("{ {x} } {%x {{ '}}' }} {{ \"%}\" }} }}")
add("{%+ if x %}a{% endif +%} b", x=1)

# --- if / elif / else ---
for x in [0, 1, 2, 3, "", "a", None, [], [0], {}, 0.0]:
    add("{% if x == 1 %}one{% elif x == 2 %}two{% elif x %}truthy{% else %}falsy{% endif %}", x=x)
add("{% if a %}{% if b %}ab{% else %}a{% endif %}{% else %}{% if b %}b{% endif %}{% endif %}", a=1, b=0)
add("{% if a %}{% if b %}ab{% else %}a{% endif %}{% else %}{% if b %}b{% endif %}{% endif %}", a=0, b=1)

# --- set, and where it reaches ---
add("{% set a = 3 %}{% if a %}{% set a = a + 1 %}{% set b = 'in' %}{% endif %}{{ a }}{{ b }}")
add("{% set x = 1 %}{% for i in [1, 2] %}{% set x = x + i %}{{ x }}{% endfor %}{{ x }}")
add("{% set n = v | int // 12 %}{% set m = (v | int % 12) %}{{ n }}-{{ m }}", v="38")

# --- for ---
add("{% for x in xs %}{{ loop.index }}/{{ loop.length }}:{{ x }}{% if not loop.last %}, {% endif %}{% else %}none{% endfor %}", xs=["a", "b", "c"])
add("{% for x in xs %}{{ x }}{% else %}none{% endfor %}", xs=[])
add("{% for x in missing %}{{ x }}{% else %}none{% endfor %}")
add("{% for k, v in d.items() %}{{ k }}={{ v }};{% endfor %}{% for k in d %}{{ k }}{% endfor %}", d={"a": 1, "b": 2})
add("{% for c in 'héllo' %}{{ loop.index0 }}{{ c }}{{ loop.first }}{{ loop.revindex }}{% endfor %}")
add("{% for x in 5 %}{{ x }}{% endfor %}")

# --- arithmetic, as Python does it ---
nums = [0, 1, 2, 7, -7, 12, 2.5, -2.5, True]
for a, b in itertools.product(nums, nums):
    for op in ["+", "-", "*", "/", "//", "%"]:
        add("{{ a %s b }}" % op, a=a, b=b)
for a, b in [(2, 10), (2, -1), (2.0, 3), (9, 0.5), (0, 0)]:
    add("{{ a ** b }}", a=a, b=b)
add("{{ -a }}{{ +a }}{{ - a | int }}{{ -2 ** 2 }}{{ 2 * 3 + 4 }}{{ 2 + 3 * 4 }}{{ (2 + 3) * 4 }}{{ 7 // 2 * 2 }}", a="5")
add("{{ 'a' + 'b' }}{{ 'ab' * 3 }}{{ 3 * 'ab' }}{{ [1] + [2] }}{{ 'x' ~ 1 ~ none ~ 2.0 ~ missing }}")
add("{{ 1 ~ 2 + 3 }}{{ 1 + 2 ~ 3 }}{{ 2 * 3 ~ 4 }}")
for t in ["{{ 'a' + 1 }}", "{{ missing + 1 }}", "{{ 1 / 0 }}", "{{ 1 // 0 }}", "{{ 1 % 0 }}", "{{ none + 1 }}", "{{ 'a' - 'b' }}", "{{ [1] * 'a' }}"]:
    add(t)

# --- comparison, in, and / or / not ---
vals = [None, True, False, 0, 1, 1.0, 2, "", "a", "b", "1", [], [1], [1, 2]]
for a, b in itertools.product(vals, vals):
    add("{{ a == b }} {{ a != b }} {{ a in [b] }} {{ a and b }} {{ a or b }} {{ not a }}", a=a, b=b)
for a, b in itertools.product([0, 1, 2.5, True, "a", "b", "ab", [1], [1, 2], [2]], repeat=2):
    add("{{ a < b }} {{ a <= b }} {{ a > b }} {{ a >= b }}", a=a, b=b)
add("{{ 1 < 2 < 3 }}{{ 1 < 3 < 2 }}{{ 1 == 1 != 2 }}{{ 'a' in 'cat' }}{{ 'x' not in 'cat' }}{{ 'k' in d }}{{ 2 in [1, 2] }}{{ ' ' in missing }}", d={"k": 1})
add("{{ missing == '' }}{{ missing != '' }}{{ missing == none }}{{ missing is none }}{{ missing == missing2 }}{{ not missing }}")
for t in ["{{ 1 in 'abc' }}", "{{ 'a' < 1 }}", "{{ none < 1 }}", "{{ missing < 1 }}", "{{ 'a' in 5 }}"]:
    add(t)
add("{{ 'yes' if a else 'no' }}{{ 'only' if a }}|{{ 1 if a else 2 if b else 3 }}", a=0, b=1)
add("{{ 'yes' if a else 'no' }}{{ 'only' if a }}|{{ 1 if a else 2 if b else 3 }}", a=1, b=0)
add("{{ a or 'fallback' }}{{ a and a.upper() }}{{ (a or 'x').upper() }}", a="")
add("{{ not a is defined }}{{ not a == 1 }}{{ not a and b }}{{ not (a and b) }}", a=1, b=0)

# --- tests ---
for v in [None, True, False, 0, 3, 4, 2.0, "", "s", [], [1], {"a": 1}]:
    add("{{ v is defined }}{{ v is undefined }}{{ v is none }}{{ v is not none }}{{ v is string }}{{ v is number }}"
        "{{ v is integer }}{{ v is float }}{{ v is boolean }}{{ v is true }}{{ v is false }}{{ v is mapping }}"
        "{{ v is sequence }}{{ v is iterable }}", v=v)
add("{{ nope is defined }}{{ nope is undefined }}{{ nope is none }}{{ nope is not none }}{{ nope is string }}{{ nope is iterable }}")
add("{{ 9 is divisibleby 3 }}{{ 9 is divisibleby(2) }}{{ 3 is odd }}{{ 3 is even }}{{ 3 is eq 3 }}{{ 3 is ne(3) }}{{ 3 is lt 4 }}{{ 3 is ge 4 }}{{ 'a' is in 'cat' }}")
add("{{ x is defined and x is not none and x != '' }}|{{ x is defined and x and y is defined and y }}", x="v")
add("{{ x is defined and x is not none and x != '' }}|{{ x is defined and x and y is defined and y }}", x="v", y="w")
add("{{ x is defined and x is not none and x != '' }}|{{ x is defined and x and y is defined and y }}", x="")
add("{{ x is defined and x is not none and x != '' }}|{{ x is defined and x and y is defined and y }}", x=None)
add("{{ x is defined and x is not none and x != '' }}|{{ x is defined and x and y is defined and y }}")
add("{{ 1 + 1 is number }}{{ 'a' ~ 'b' is string }}{{ -1 is number }}")

# --- filters ---
for v in ["38", " 38 ", "38.9", "-7.9", "1e3", "abc", "", "1_000", "+5", None, True, 12, 12.7, -12.7, [1], "0x1f", "inf", "nan", "１２"]:
    add("{{ v | int }}|{{ v | int(-1) }}|{{ v | float }}|{{ v | float(default=9.5) }}|{{ v | string }}|{{ v | int // 12 }}|{{ v | int % 12 }}", v=v)
for f in ["int", "float", "int(3)", "string", "default('d')", "d(5)", "join(',')", "default(3) | int", "length", "count", "upper", "lower", "trim", "first", "last", "list", "round", "abs", "capitalize", "replace('a', 'b')", "safe", "string | length"]:
    add("[{{ nope | %s }}]" % f)
    add("[{{ v | %s }}]" % f, v=None)
    add("[{{ v | %s }}]" % f, v=7)
    add("[{{ v | %s }}]" % f, v="héllo Wörld")
for v in [15, 25, 35, -15, 1234, 5]:
    add("{{ v | round(-1) }}|{{ v | round(-2) }}|{{ v | round(1) }}", v=v)
for v in ["", "x", 0, None, False, []]:
    add("[{{ v | default('d') }}][{{ v | default('d', true) }}][{{ v | default('d', boolean=true) }}]", v=v)
add("{{ '  pad  ' | trim }}|{{ 'xxaxx' | trim('x') }}|{{ 'MiXed' | upper }}{{ 'MiXed' | lower }}{{ 'mIXED word' | capitalize }}|{{ 'a-b-c' | replace('-', '+') }}{{ 'a-b-c' | replace('-', '+', 1) }}")
add("{{ [1, 'a', none] | join }}|{{ [1, 2] | join(', ') }}|{{ 'abc' | join('-') }}|{{ 'héllo' | length }}{{ [1, 2] | count }}{{ d | length }}", d={"a": 1})
for v in [2.5, 3.5, 2.675, 1.005, -2.5, 7, 0.5, 1234.5678]:
    add("{{ v | round }}|{{ v | round(2) }}|{{ v | round(1, 'floor') }}|{{ v | round(1, 'ceil') }}|{{ v | round | int }}", v=v)
add("{{ -3 | abs }}{{ -3.5 | abs }}{{ [1, 2, 3] | first }}{{ [1, 2, 3] | last }}{{ 'abc' | first }}[{{ [] | first }}]{{ 'ab' | list }}{{ 'x' | safe }}")
add("{{ a | int + b | int }}{{ a | int * 2 }}{{ a | default(b) | int }}", a="3", b="4")

# --- subscripts, slices, methods ---
add("{{ s[0] }}{{ s[-1] }}[{{ s[99] }}]{{ s[1:3] }}{{ s[:2] }}{{ s[2:] }}{{ s[::-1] }}{{ s[::2] }}{{ s[-2:] }}{{ s[5:1:-2] }}", s="héllo wörld")
add("{{ l[0] }}{{ l[-1] }}[{{ l[9] }}]{{ l[1:] }}{{ l[:-1] }}{{ l[::-1] }}{{ d['a'] }}{{ d.a }}[{{ d.zz }}][{{ d['zz'] }}]{{ d.get('zz', 'dflt') }}{{ d.get('a') }}{{ d.get('zz') }}", l=[1, 2, 3], d={"a": "A"})
for when in ["2026-10-13 23:10:00", "2026-10-13", "", None, "a b c  d"]:
    add("{% set d = w.split(' ')[0] if w is defined and w is not none and ' ' in w else w %}[{{ d }}]", w=when)
add("{% set d = w.split(' ')[0] if w is defined and w is not none and ' ' in w else w %}[{{ d }}]")
add("{{ s.split() }}{{ s.split(' ') }}{{ s.split(',') }}{{ s.split(None, 1) }}{{ s.split(' ', 1) }}{{ s.split(sep=',') }}", s="  a b,c   d ")
add("[{{ s.strip() }}][{{ s.lstrip() }}][{{ s.rstrip() }}][{{ s.strip('x ') }}]{{ s.startswith('  x') }}{{ s.endswith('y') }}{{ s.replace('x', 'y') }}{{ s.upper() }}{{ s.lower() }}{{ s.find('A') }}{{ s.find('q') }}{{ s.count('x') }}", s="  xxAxx ")
add("{{ ', '.join(['a', 'b']) }}{{ '7'.zfill(3) }}{{ '-7'.zfill(4) }}{{ '123'.isdigit() }}{{ '12a'.isdigit() }}{{ 'héllo wörld'.capitalize() }}{{ d.keys() | list }}{{ d.values() | list }}", d={"a": 1})
for t in ["{{ missing.split(' ') }}", "{{ missing[0] }}", "{{ missing.a.b }}", "{{ none.x.y }}", "{{ 'a'.nothere() }}", "{{ 5.split() }}", "{{ 'abc'.split('') }}"]:
    add(t)

# --- a prompt of the shape this service is given ---
PROMPT = (
    "你是{% if not is_sh %}甲乙联合{% else %}甲方{% endif %}的客服。 "
    "{%- if plate is defined and plate is not none and plate != \"\" -%} - Q: 哪辆车 A: 是您的爱车{{ plate }}。 {%- endif -%} "
    "{%- if plate is defined and plate and name is defined and name -%} - Q: 车主是谁 A: 车牌号为 {{ plate }} 的车主是 {{ name }}。 {%- endif -%} - 结尾。  "
    "{% if is_sh %} - 本地。 {% else %} - 外地。 {% endif %}  ##任务信息: 客户信息:  "
    "{%- if plate is defined and plate is not none and plate != \"\" -%} 车牌号:{{plate}}， {%- else -%} 车牌号:未获取 {%- endif -%}   "
    "{%- if months is defined and months is not none and months != \"\" -%} 车辆年限:{% set years = (months | int // 12) %}{% set rest = (months | int % 12) %}{{ years }}年{{ rest }}个月， {%- else -%} 车辆年限:未获取 {%- endif -%}  "
    "{%- set day = now.split(' ')[0] if now is defined and now is not none and ' ' in now else now -%} 当前时间:{{day}}， 开场白:\"{{start}}\" \"\"\""
)
rng = random.Random(7)
choices = {
    "is_sh": [True, False, None, 0, 1, "", "yes"],
    "plate": ["沪A12345", "", None, 0],
    "name": ["王先生", "", None],
    "months": ["38", 38, "0", "", None, "abc", 12.9, "143"],
    "now": ["2026-10-13 23:10:00", "2026-10-13", "", None],
    "start": ["您好，这里是客服。", "", None],
}
for _ in range(240):
    variables = {}
    for key, options in choices.items():
        if rng.random() < 0.8:
            variables[key] = rng.choice(options)
    add(PROMPT, **variables)
add(PROMPT)

# --- things that are not supported must be refused, not guessed at ---
unsupported = ["{% macro m() %}{% endmacro %}", "{% include 'x' %}", "{% set a %}x{% endset %}", "{{ x | nosuchfilter }}",
               "{{ x is nosuchtest }}", "{% if x %}open", "{{ x", "{% endif %}", "{% for x in y %}open", "{{ }}", "{{ 1 + }}", "{{ a b }}"]

env = jinja2.Environment()
out = []
for template, variables in cases:
    case = {"template": template, "vars": variables}
    try:
        case["want"] = env.from_string(template).render(**variables)
    except Exception as err:  # the Go side must fail too; what it says is its own
        case["error"] = type(err).__name__
    out.append(case)
for template in unsupported:
    out.append({"template": template, "vars": {}, "error": "unsupported"})

here = os.path.dirname(os.path.abspath(__file__))
with open(os.path.join(here, "cases.json"), "w", encoding="utf-8") as f:
    json.dump({"jinja2": jinja2.__version__, "cases": out}, f, ensure_ascii=False, indent=0)
print(len(out), "cases,", sum(1 for c in out if "error" in c), "of them errors, from Jinja2", jinja2.__version__)
