import importlib.machinery
import importlib.util
import pathlib
import unittest


path = pathlib.Path(__file__).with_name("check-sync-parity.py")
loader = importlib.machinery.SourceFileLoader("sync_parity", str(path))
spec = importlib.util.spec_from_loader(loader.name, loader)
parity = importlib.util.module_from_spec(spec)
loader.exec_module(parity)


class MailboxNameTest(unittest.TestCase):
    def test_modified_utf7_round_trip_and_list_quoting(self):
        name = "Café & Dogs"
        self.assertEqual(parity.decode_utf7(parity.encode_utf7(name)), name)
        self.assertEqual(parity.mailbox_name(b'* LIST () "/" "Caf&AOk- &- Dogs"'), name)


if __name__ == "__main__":
    unittest.main()
