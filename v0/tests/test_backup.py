import os
import tempfile
import sqlite3
from unittest.mock import MagicMock, patch

import pytest
import backup


def test_validate_backup_config_raises_without_encrypt_key(monkeypatch):
    monkeypatch.setattr(backup, "BACKUP_REPO", "GMISWE/ieops-mem-backup")
    monkeypatch.setattr(backup, "BACKUP_ENCRYPT_KEY", None)
    with pytest.raises(RuntimeError, match="BACKUP_ENCRYPT_KEY"):
        backup.validate_backup_config()


def test_validate_backup_config_ok_when_no_repo(monkeypatch):
    monkeypatch.setattr(backup, "BACKUP_REPO", None)
    monkeypatch.setattr(backup, "BACKUP_ENCRYPT_KEY", None)
    backup.validate_backup_config()  # should not raise


def test_do_backup_skips_when_no_token(monkeypatch, caplog):
    monkeypatch.setattr(backup, "GITHUB_TOKEN", None)
    monkeypatch.setattr(backup, "BACKUP_REPO", "GMISWE/test-backup")
    import logging
    with caplog.at_level(logging.INFO, logger="backup"):
        backup._do_backup()
    assert "skipped" in caplog.text


def test_do_backup_calls_github(monkeypatch, tmp_path):
    monkeypatch.setattr(backup, "GITHUB_TOKEN", "fake-token")
    monkeypatch.setattr(backup, "BACKUP_REPO", "GMISWE/test-backup")
    monkeypatch.setattr(backup, "BACKUP_BRANCH", "backups")
    monkeypatch.setattr(backup, "BACKUP_ENCRYPT_KEY", "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p")

    # Patch DB path to a real temp DB (pyrage/Github imported lazily inside _do_backup)
    db_path = str(tmp_path / "test.db")
    sqlite3.connect(db_path).close()
    import db as _db
    monkeypatch.setattr(_db, "DB_PATH", db_path)

    mock_encrypted = b"encrypted-data"
    mock_repo = MagicMock()
    mock_repo.get_contents.side_effect = Exception("not found")

    mock_pyrage = MagicMock()
    mock_pyrage.x25519.Recipient.from_str.return_value = MagicMock()
    mock_pyrage.encrypt.return_value = mock_encrypted

    mock_github_module = MagicMock()
    mock_github_module.Github.return_value.get_repo.return_value = mock_repo

    monkeypatch.setitem(__import__("sys").modules, "pyrage", mock_pyrage)
    monkeypatch.setitem(__import__("sys").modules, "github", mock_github_module)

    backup._do_backup()

    mock_repo.create_file.assert_called_once()
    call_args = mock_repo.create_file.call_args
    assert call_args[0][2] == mock_encrypted  # encrypted content passed
