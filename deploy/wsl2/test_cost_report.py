import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('report', Path(__file__).with_name('test-report.py'))
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)


class CostReportTests(unittest.TestCase):
    def test_repeated_queries_and_step_copies_do_not_duplicate_charges(self):
        api = {'scope': 'api', 'step': 'video_wait', 'provider_task_id': 'job-1',
               'usage': {'thirdPartyConsumeMoney': '1.25', 'consumeCoins': None}}
        result = report.summarize_costs([api, api, {**api, 'scope': 'step'}])
        self.assertEqual(result['reported_field_subtotals'], {'thirdPartyConsumeMoney': '1.25'})
        self.assertEqual(len(result['jobs']), 1)

    def test_missing_fees_and_local_compute_do_not_turn_into_zero(self):
        api = {'scope': 'api', 'step': 'tts', 'provider_task_id': 'tts-1',
               'usage': {'consumeMoney': None, 'billingSeconds': '10'}}
        result = report.summarize_costs([api])
        self.assertEqual(result['jobs'][0]['cost_status'], 'unknown')
        self.assertEqual(result['reported_field_subtotals'], {})
        self.assertIsNone(result['local_compute_money'])


if __name__ == '__main__':
    unittest.main()
