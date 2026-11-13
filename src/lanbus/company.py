"""Shared company membership credentials for symmetric agent nodes."""

import hashlib
import hmac
import json
import os
import uuid

from .peer import _atomic_json


def company_proof(secret, company_id, peer_id, nonce):
    value = f"{company_id}:{peer_id}:{nonce}".encode("utf-8")
    return hmac.new(bytes.fromhex(secret), value, hashlib.sha256).hexdigest()


class CompanyStore:
    def __init__(self, data_dir):
        self.path = os.path.join(data_dir, "company.json")

    def load(self):
        try:
            with open(self.path, encoding="utf-8") as handle:
                value = json.load(handle)
            if not isinstance(value, dict) or not value.get("company_id") or not value.get("secret"):
                return None
            bytes.fromhex(value["secret"])
            return value
        except FileNotFoundError:
            return None
        except Exception as exc:
            raise ValueError(f"公司凭证文件损坏：{self.path}") from exc

    def save(self, value):
        _atomic_json(self.path, value, private=True)
        return value

    def create(self, name):
        value = {"company_id": uuid.uuid4().hex[:16], "company_name": name.strip() or "Lanbus 公司",
                 "secret": uuid.uuid4().hex + uuid.uuid4().hex, "members": []}
        return self.save(value)

    def clear(self):
        try:
            os.unlink(self.path)
        except FileNotFoundError:
            pass
