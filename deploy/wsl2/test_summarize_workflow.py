import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

class ReportTests(unittest.TestCase):
    def test_unique_orders_have_per_step_amounts_without_currency_invention(self):
        bill={'key':'order-1','step':'F06/p1-c1','usage':{'thirdPartyConsumeMoney':'2.613'}}
        with tempfile.TemporaryDirectory() as d:
            folder=Path(d);state={'id':'example','status':'succeeded','duration':9,'billing':[bill,bill], 'steps':[{'id':'F06/p1-c1','status':'succeeded','seconds':223.345,'attempt':1,'cost_status':'reported','provider_task_id':'task-1','usage':bill['usage']}]}
            (folder/'state.json').write_text(json.dumps(state))
            subprocess.run([sys.executable,str(Path(__file__).with_name('summarize-workflow.py')),str(folder/'state.json'),str(folder/'report.md')],check=True,capture_output=True)
            text=(folder/'report.md').read_text()
            row=next(line for line in text.splitlines() if line.startswith('| F06/p1-c1'))
            self.assertIn('thirdPartyConsumeMoney=2.613',row)
            self.assertIn('223.345',row)
            self.assertIn('**2.613**',text)
            self.assertNotIn('**5.226**',text)
            self.assertNotIn('CNY',text)

if __name__=='__main__':unittest.main()
