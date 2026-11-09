"""Deployment login checks must tolerate a retired account without exposing secrets."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("verify_deployment", Path(__file__).with_name("verify.py"))
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class DeploymentLoginTests(unittest.TestCase):
    def test_reads_complete_accounts_without_mixing_passwords(self):
        text = "URL: http://localhost:8080/\nEmail: retired@test\nPassword: old-secret\nEmail: incomplete@test\nEmail: active@test\nPassword: new-secret\n"
        self.assertEqual(verify.parse_access_accounts(text), [
            {"email": "retired@test", "password": "old-secret"},
            {"email": "active@test", "password": "new-secret"},
        ])

    def test_uses_next_saved_account_only_when_login_is_rejected(self):
        accounts = [{"email": "old@test", "password": "old"}, {"email": "new@test", "password": "new"}]
        with patch.object(verify, "request", side_effect=[(401, {}), (200, {"data": {"tokens": {"access_token": "token"}}})]) as request:
            self.assertEqual(verify.login_saved_account(accounts), "token")
        self.assertEqual(request.call_args_list[1].args[1], accounts[1])

    def test_keeps_first_account_when_it_is_still_valid(self):
        with patch.object(verify, "request", return_value=(200, {"data": {"tokens": {"access_token": "first"}}})) as request:
            self.assertEqual(verify.login_saved_account([{"email": "a", "password": "b"}, {"email": "c", "password": "d"}]), "first")
            self.assertEqual(request.call_count, 1)

    def test_does_not_hide_server_errors_by_switching_accounts(self):
        with patch.object(verify, "request", return_value=(500, {})) as request:
            with self.assertRaisesRegex(RuntimeError, "HTTP 500"):
                verify.login_saved_account([{"email": "a", "password": "b"}])
            self.assertEqual(request.call_count, 1)

    def test_failure_does_not_include_credentials(self):
        with patch.object(verify, "request", return_value=(401, {})):
            with self.assertRaises(RuntimeError) as error:
                verify.login_saved_account([{"email": "private@test", "password": "private-secret"}])
            self.assertNotIn("private", str(error.exception))


if __name__ == "__main__":
    unittest.main()
