import tempfile
from pathlib import Path
import unittest
from unittest.mock import Mock
import domain_bridge as b

class BridgeTests(unittest.TestCase):
    def api(self,proxy=None,domain='buy.example.test'):
        def call(resource,body=None,method=None):
            if resource=='site':return {'domain':'control.example.test'}
            if resource=='certificates':return [{'id':'cert','domain':domain,'state':'ready'}]
            if resource=='reverse-proxies' and body is None:return [] if proxy is None else [proxy]
            return {'id':'proxy'}
        return Mock(side_effect=call)
    def test_reuses_ready_certificate_without_secret(self):
        with tempfile.TemporaryDirectory() as tmp:
            api=self.api();self.assertTrue(b.configure(Path(tmp),'buy.example.test',18088,api))
            api.assert_any_call('reverse-proxies',{'domain':'buy.example.test','upstream':'http://127.0.0.1:18088','certificate_id':'cert','enabled':True,'options':{'connect_timeout':10,'read_timeout':60,'body_limit_mib':2,'websocket':False}},'POST')
    def test_does_not_hijack_existing_domains(self):
        for domain,proxy in [('control.example.test',None),('buy.example.test',{'domain':'buy.example.test','upstream':'https://another.example.test'})]:
            with tempfile.TemporaryDirectory() as tmp,self.assertRaises(ValueError):b.configure(Path(tmp),domain,18088,self.api(proxy))
    def test_keeps_matching_existing_proxy(self):
        proxy={'id':'same','domain':'buy.example.test','upstream':'http://127.0.0.1:18088','enabled':True,'certificate_id':'cert','version':2}
        with tempfile.TemporaryDirectory() as tmp:
            api=self.api(proxy);b.configure(Path(tmp),'buy.example.test',18088,api)
            self.assertFalse(any(c.kwargs or len(c.args)>1 for c in api.call_args_list))

if __name__=='__main__':unittest.main()
