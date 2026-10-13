import copy
import unittest
from validate_package import validate, PROJECT

class IsolationValidation(unittest.TestCase):
    def valid(self):
        return {"name": PROJECT, "services": {name: {"image": PROJECT + ("-activecall:pinned" if name == "activecall" else ":local"), "networks": {"simulator": {}}, "volumes": [{"type":"volume","source":"run-data"}]} for name in ("config","simulator","activecall")}, "networks":{"simulator":{"name":PROJECT+"_simulator"}},"volumes":{"run-data":{"name":PROJECT+"_run-data"}}}
    def test_valid(self): self.assertEqual(validate(self.valid()), [])
    def test_host_bind_mount_rejected(self):
        c=self.valid();c["services"]["activecall"]["volumes"]=[{"type":"bind","source":"/etc/production"}];self.assertTrue(validate(c))
    def test_public_ports_rejected(self):
        c=self.valid();c["services"]["simulator"]["ports"]=[{"host_ip":"0.0.0.0","target":18865,"published":"18865"}];self.assertTrue(validate(c))
    def test_shared_resources_rejected(self):
        c=self.valid();c["volumes"]["run-data"]["external"]=True;self.assertTrue(validate(c))
    def test_existing_container_name_rejected(self):
        c=self.valid();c["services"]["activecall"]["container_name"]="active-call";self.assertTrue(validate(c))

if __name__ == '__main__': unittest.main()
