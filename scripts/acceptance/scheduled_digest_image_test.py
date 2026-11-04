import base64
import hashlib
import importlib.util
import json
from pathlib import Path
import socket
import tempfile
import threading
import unittest


MODULE = Path(__file__).with_name("scheduled_digest_image.py")
SPEC = importlib.util.spec_from_file_location("scheduled_digest_image", MODULE)
SUBJECT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SUBJECT)


class ScheduledDigestImageTest(unittest.TestCase):
    def test_identity_binds_exact_app_tuple(self):
        registration = SUBJECT.registration()
        identity = SUBJECT.service_identity()
        self.assertEqual(
            registration["format"],
            "portal.managed-backend-registration/1-dev.1",
        )
        self.assertEqual(
            registration["metadata"]["identity"]["deployment_id"],
            SUBJECT.DEPLOYMENT_ID,
        )
        self.assertEqual(
            registration["metadata"]["identity"]["controller_epoch"],
            SUBJECT.CONTROLLER_EPOCH,
        )
        self.assertEqual(
            registration["metadata"]["instance_id"], SUBJECT.INSTANCE_ID
        )
        key = base64.b64decode(registration["signing_key"], validate=True)
        self.assertEqual(len(key), 32)
        self.assertEqual(
            registration["metadata"]["key_id"], hashlib.sha256(key).hexdigest()
        )
        self.assertEqual(identity["app_id"], "scheduled-digest")
        self.assertEqual(
            identity["installation_id"],
            registration["metadata"]["identity"]["installation_id"],
        )
        self.assertEqual(
            identity["generation"],
            registration["metadata"]["identity"]["generation"],
        )
        self.assertEqual(
            identity["policy_revision"],
            registration["metadata"]["identity"]["policy_revision"],
        )
        self.assertEqual(
            identity["release_sha256"],
            registration["metadata"]["identity"]["release_sha256"],
        )
        self.assertEqual(identity["resources"], ["reports.read", "tasks.schedule"])

    def test_handshake_answer_binds_challenge_and_boot(self):
        now = 1_800_000_000
        challenge = SUBJECT.handshake_challenge(now=now)
        raw = json.loads(SUBJECT._decode_token_part(challenge.split(".", 1)[0]))
        raw.update(
            {
                "kind": "answer",
                "boot": "a" * 32,
                "challenge_sha256": hashlib.sha256(
                    challenge.encode("ascii")
                ).hexdigest(),
            }
        )
        answer = SUBJECT.encode_handshake(raw)
        self.assertEqual(
            SUBJECT.verify_handshake_answer(
                challenge, {"answer": answer}, now=now + 1
            ),
            "a" * 32,
        )
        for changed in (
            {"answer": answer, "redirect": "private"},
            {"answer": answer.replace(".", "..", 1)},
        ):
            with self.assertRaises(ValueError):
                SUBJECT.verify_handshake_answer(challenge, changed, now=now + 1)
        with self.assertRaises(ValueError):
            SUBJECT.verify_handshake_answer(challenge, {"answer": answer}, now=now + 30)

    def test_task_body_is_canonical_and_closed(self):
        raw = SUBJECT.task_body(
            "10000000-0000-4000-8000-000000000004",
            "digest:exact-image:1",
            [41, 42],
        )
        self.assertEqual(
            raw,
            b'{"format":"portal.app-task.request/1-dev.1","occurrence_id":"digest:exact-image:1","payload":{"reportIds":[41,42],"scheduleId":"daily"},"task_id":"10000000-0000-4000-8000-000000000004"}',
        )
        value = json.loads(raw)
        self.assertEqual(set(value), {"format", "task_id", "occurrence_id", "payload"})

    def test_response_requires_exact_protocol(self):
        valid = {
            "format": "portal.app-task.response/1-dev.1",
            "outcome": "succeeded",
        }
        self.assertEqual(SUBJECT.decode_response(json.dumps(valid).encode()), valid)
        for invalid in (
            {},
            {"format": "portal.app-task.response/2"},
            [],
        ):
            with self.assertRaises(ValueError):
                SUBJECT.decode_response(json.dumps(invalid).encode())

    def test_image_reference_requires_immutable_local_digest(self):
        valid = "localhost:5000/scheduled-digest-exact@sha256:" + "a" * 64
        self.assertIsNotNone(SUBJECT.IMAGE.fullmatch(valid))
        for invalid in (
            "scheduled-digest:latest",
            "localhost:5000/scheduled-digest-exact:fixture",
            "other.invalid/scheduled-digest-exact@sha256:" + "a" * 64,
            "localhost:5000/scheduled-digest-exact@sha256:short",
        ):
            self.assertIsNone(SUBJECT.IMAGE.fullmatch(invalid))

    def test_request_accepts_pathlib_unix_socket(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "app.sock"
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            listener.bind(str(path))
            listener.listen(1)

            def serve():
                connection, _ = listener.accept()
                try:
                    connection.recv(4096)
                    connection.sendall(
                        b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n"
                        b"Connection: close\r\n\r\n{}"
                    )
                finally:
                    connection.close()

            worker = threading.Thread(target=serve)
            worker.start()
            try:
                status, raw = SUBJECT.request(path, "GET", "/health/ready")
            finally:
                worker.join(timeout=5)
                listener.close()
            self.assertEqual(status, 200)
            self.assertEqual(raw, b"{}")


if __name__ == "__main__":
    unittest.main()
