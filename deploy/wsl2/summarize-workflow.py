"""Produce a readable report from an exported workflow state; no API calls."""
import argparse
from collections import defaultdict
from decimal import Decimal
import json
from pathlib import Path

p=argparse.ArgumentParser();p.add_argument('state');p.add_argument('output');args=p.parse_args()
s=json.loads(Path(args.state).read_text(encoding='utf-8-sig'))
totals=defaultdict(Decimal);by_step=defaultdict(lambda:defaultdict(Decimal));unknown_steps=set();known=0;unknown=0;seen=set()
for bill in s.get('billing',[]):
    key=bill['key']
    if key in seen:continue
    seen.add(key);usage=bill.get('usage') or {};priced=False
    for field in ['thirdPartyConsumeMoney','consumeMoney','consumeCoins']:
        value=usage.get(field)
        if value is not None:
            amount=Decimal(str(value));totals[field]+=amount;by_step[bill.get('step','')][field]+=amount;priced=True
    if priced:known+=1
    else:unknown+=1;unknown_steps.add(bill.get('step',''))
lines=['# 真实运行成本与耗时报告','',f"运行：`{s['id']}`；状态：{s['status']}；时长：{s.get('duration',0):.6f} 秒。",'',
       '仅汇总供应商回传的原字段，不假设币种，也不将 tokens 自动换算金额。不同计价字段不能相加；下列金额不是含全部未知费用的完整总价。','']
if s.get('reused_from'):
    lines.extend([f"本运行复用 `{s['reused_from']}` 的部分产物；本表只记录本次调用，复用素材的历史成本仍保留在原运行，不能据此认为整条生产链免费。",''])
for field,value in totals.items():lines.append(f'- `{field}`：**{value}**（供应商原单位，未声明币种）。')
lines.extend([f'- 已返回计价字段的唯一调用：{known}；未返回金额/价格：{unknown}。','',
              '## 节点实际记录','',
              '| 节点 | 状态 | 累计秒数 | 尝试数 | 费用状态 | 本步骤已知计价字段（原单位） | 回单/用量 |','| --- | --- | ---: | ---: | --- | --- | --- |'])
for step in s.get('steps',[]):
    usage=json.dumps(step.get('usage') or {},ensure_ascii=False,separators=(',',':'))
    reference=step.get('provider_task_id') or (usage if usage!='{}' else '—')
    amounts='；'.join(f'{field}={value}' for field,value in by_step[step['id']].items())
    if step['id'] in unknown_steps:amounts+=( '；' if amounts else '')+'另有未返回金额的调用'
    if not amounts:amounts={'reused':'本次无新调用；历史成本见来源','local_compute':'本机计算；子订单另列'}.get(step['cost_status'],'未返回金额')
    lines.append(f"| {step['id']} | {step['status']} | {step['seconds']:.3f} | {step['attempt']} | {step['cost_status']} | {amounts} | {reference} |")
lines.extend(['','## 计量边界','',
              '- F06父步骤包含子订单处理，不能把父子耗时直接相加。累计节点时间包含已记录失败尝试，供应商taskCostTime不替代本机墙钟时间。',
              '- local_compute表示本机计算，不表示本机资源没有成本；reused表示本次无新增该供应商调用。',
              '- 确认等待、停机和排错期间的墙钟时间不等于实际生成处理时间。保留原记录，不倒填历史耗时。',
              '- 目录预生成、单独诊断与其他运行的调用不包含在本运行汇总；费用未知时须另查供应商账单。',
              '- 所有成功/失败/未知付费尝试仍以JSON中的attempts、orders和billing为详细依据。',''])
if s['id']=='9add337b-559f-4273-921e-79ffb8b7d49b':
    lines.extend(['## 本次排错记录的限制','',
                  'F04/p1首次中断发生于旧版记录实现，当前累计秒数只包含恢复后记录的处理，不能代表首轮等待总耗时。F05含一次HTTP400及一次成功调用；独立推理参数诊断有超时且价格未知，不在本运行金额中。F10第二次使用现成素材作本地保守分镜修复，没有新增模型请求。',
                  '', '本片三个波形相关值0.0647、0.1751、0.1096均仅为音轨诊断，不能判定嘴型/声音/身份效果通过或不通过。',''])
Path(args.output).write_text('\n'.join(lines),encoding='utf-8')
print('Readable workflow report written.')
