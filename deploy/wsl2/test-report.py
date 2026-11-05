"""Export measured steps and provider accounting for a real authenticated task."""
import argparse
import csv
from decimal import Decimal, InvalidOperation
import importlib.util
import json
from datetime import datetime
from pathlib import Path


def summarize_costs(events):
    # A query or retry can return the same bill repeatedly. Count each provider
    # task once, and never add different money/coin fields to one fake total.
    tasks = {}
    for event in events:
        if event.get('scope') != 'api' or not event.get('provider_task_id') or not event.get('usage'):
            continue
        tasks[event['provider_task_id']] = event
    subtotals = {}
    jobs = []
    for task_id, event in tasks.items():
        values = {}
        for field in ('thirdPartyConsumeMoney', 'consumeMoney', 'consumeCoins'):
            raw = event['usage'].get(field)
            if raw is None:
                continue
            try:
                amount = Decimal(str(raw))
                if not amount.is_finite() or amount < 0:
                    continue
            except InvalidOperation:
                continue
            values[field] = str(amount)
            subtotals[field] = subtotals.get(field, Decimal(0)) + amount
        jobs.append({'provider_task_id': task_id, 'step': event['step'], 'shot_id': event.get('shot_id', ''),
                     'values': values, 'usage': event['usage'], 'cost_status': 'reported' if values else 'unknown'})
    return {'jobs': jobs, 'reported_field_subtotals': {k: str(v) for k, v in subtotals.items()},
            'currency': 'not_confirmed', 'local_compute_money': None,
            'note': '供应商金额与币点原字段分开汇总；币种未经确认，未返回的费用不记为零。本机计算费用未折算。'}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--run', default='final-e2e')
    args = parser.parse_args()
    if not args.run or not all(c.isalnum() or c in '-_' for c in args.run):
        raise ValueError('invalid run name')
    spec = importlib.util.spec_from_file_location('live_test', Path(__file__).with_name('live-test.py'))
    live = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(live)
    root = live.OUTPUT / args.run
    entry = json.loads((root / 'request.json').read_text())
    token = live.login()
    status, task = live.request('/api/v1/avatar-mvp/tasks/' + entry['task_id'], token=token)
    if status != 200:
        raise RuntimeError('task request HTTP ' + str(status))
    task = task.get('data', task)
    safe = live.safe_task(task)
    (root / 'report.json').write_text(json.dumps(safe, ensure_ascii=False, indent=2))
    events = task.get('operations', [])
    costs = summarize_costs(events)
    (root / 'costs.json').write_text(json.dumps(costs, ensure_ascii=False, indent=2))
    fields = ('step', 'shot_id', 'action', 'started_at', 'finished_at', 'seconds', 'status',
              'http_status', 'provider_task_id', 'provider_request_id', 'provider_code',
              'response_sha256', 'cost_status', 'usage')
    steps = [event for event in events if event['scope'] == 'step']
    for filename, rows in [('steps.csv', steps), ('requests.csv', [e for e in events if e['scope'] == 'api'])]:
        with (root / filename).open('w', newline='', encoding='utf-8-sig') as out:
            writer = csv.DictWriter(out, fieldnames=fields, extrasaction='ignore')
            writer.writeheader()
            for row in rows:
                writer.writerow({**row, 'usage': json.dumps(row.get('usage', {}), ensure_ascii=False)})
    lines = ['# 真实成片测试明细', '', '任务：' + task['id'], '状态：' + task['status'],
             '视频时长：' + str(task['duration']) + ' 秒', '',
             '| 步骤 | 镜头 | 耗时（秒） | 结果 | 成本状态 / 供应商返回 |',
             '|---|---|---:|---|---|']
    for event in steps:
        usage = json.dumps(event.get('usage', {}), ensure_ascii=False)
        lines.append(f"| {event['step']} | {event.get('shot_id', '整片')} | {event['seconds']:.3f} | {event['status']} | {event['cost_status']} {usage} |")
    if task.get('completed_at'):
        wall = (datetime.fromisoformat(task['completed_at'].replace('Z', '+00:00')) -
                datetime.fromisoformat(task['created_at'].replace('Z', '+00:00'))).total_seconds()
        lines += ['', f'任务总墙钟耗时：{wall:.3f} 秒（包含排程、持久化和步骤之间的开销）。']
    lines += ['', '仅汇总不同供应商订单一次：', '', '```json', json.dumps(costs, ensure_ascii=False, indent=2), '```', '',
              'local_compute：本机计算，未折算电费/设备成本。unknown/not_reported：未取得费用，不能当作零。',
              'video_submit 与 video_wait 属于同一订单，金额通常在查询完成时返回，不能重复计费。',
              '原先 edcbb164-8fce-400b-a2ab-30335614f097 提交状态不明的费用仍待供应商核实，未计入本片已确认小计。']
    (root / 'REPORT.md').write_text('\n'.join(lines), encoding='utf-8')
    print(json.dumps({'task_id': task['id'], 'status': task['status'], 'step_count': len(steps),
                      'costs': costs, 'output': str(root)}, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
