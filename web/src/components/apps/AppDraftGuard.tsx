import { useEffect } from "react";
import { Modal } from "antd";
import { useBeforeUnload, useBlocker } from "react-router";
import { useTranslation } from "react-i18next";

// Boolean document state only: the host never receives draft contents or an
// app-authored prompt. Proceed is the user's decision and cannot await the app.
export function useAppDraftBlocker(dirty: boolean) {
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      dirty &&
      (currentLocation.pathname !== nextLocation.pathname ||
        currentLocation.search !== nextLocation.search ||
        currentLocation.hash !== nextLocation.hash),
  );
  useBeforeUnload((event) => {
    if (dirty) {
      event.preventDefault();
      event.returnValue = "";
    }
  });
  useEffect(() => {
    if (!dirty && blocker.state === "blocked") blocker.reset();
  }, [dirty, blocker]);
  return blocker;
}
export function AppDraftPrompt({
  dirty,
  blocker,
}: {
  dirty: boolean;
  blocker: ReturnType<typeof useAppDraftBlocker>;
}) {
  const { t } = useTranslation();
  return (
    <Modal
      open={dirty && blocker.state === "blocked"}
      title={t("apps.draftLeaveTitle")}
      okText={t("apps.draftLeave")}
      cancelText={t("apps.draftStay")}
      okButtonProps={{ danger: true }}
      onOk={() => {
        if (blocker.state === "blocked") blocker.proceed();
      }}
      onCancel={() => {
        if (blocker.state === "blocked") blocker.reset();
      }}
    >
      {t("apps.draftLeaveBody")}
    </Modal>
  );
}

export default function AppDraftGuard({ dirty }: { dirty: boolean }) {
  const blocker = useAppDraftBlocker(dirty);
  return <AppDraftPrompt dirty={dirty} blocker={blocker} />;
}
