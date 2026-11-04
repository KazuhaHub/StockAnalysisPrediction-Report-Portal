import importlib.util
import json
from pathlib import Path
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


if __name__ == "__main__":
    unittest.main()
