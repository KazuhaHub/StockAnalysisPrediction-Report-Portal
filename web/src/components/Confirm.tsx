import type { ReactNode } from 'react'
import { Popconfirm, type ModalFuncProps } from 'antd'
import type { HookAPI } from 'antd/es/modal/useModal'
import { useTranslation } from 'react-i18next'

// Every confirmation in the portal, inline (antd Popconfirm) or as a dialog (modal.confirm), follows
// one set of rules from the platform guidelines [Apple alerts; Geist modal]:
// - the confirming button names the action and its object ("Delete token"), never "OK" or "Yes";
// - a destructive action's button is red, and focus starts on Cancel, so a stray Enter does nothing;
// - an action that cannot be undone says so under the question.
// A guard test (confirmations.test.ts) keeps raw Popconfirm and modal.confirm out of the pages.

interface Rules {
  /** The question: what is about to happen ("Delete this token?"). */
  title: ReactNode
  /** Verb and object on the confirming button ("Delete token"). */
  okText: string
  /** Red button; on by default, since most confirmations guard something destructive. */
  danger?: boolean
  /** Adds "This cannot be undone." */
  irreversible?: boolean
  /** More about the consequence, shown before the irreversibility line. */
  description?: ReactNode
}

export default function Confirm({
  title,
  okText,
  danger = true,
  irreversible,
  description,
  onConfirm,
  onOpenChange,
  disabled,
  placement,
  children,
}: Rules & {
  onConfirm: () => void | Promise<unknown>
  onOpenChange?: (open: boolean) => void
  disabled?: boolean
  placement?: 'top' | 'bottom' | 'left' | 'right' | 'topLeft' | 'topRight' | 'bottomLeft' | 'bottomRight'
  children: ReactNode
}) {
  const { t } = useTranslation()
  const body = description || irreversible ? (
    <>
      {description}
      {description && irreversible ? ' ' : null}
      {irreversible ? t('confirm.irreversible') : null}
    </>
  ) : undefined
  return (
    <Popconfirm
      title={title}
      description={body}
      okText={okText}
      cancelText={t('common.cancel')}
      okButtonProps={{ danger }}
      cancelButtonProps={{ autoFocus: true }}
      onConfirm={onConfirm}
      onOpenChange={onOpenChange}
      disabled={disabled}
      placement={placement}
    >
      {children}
    </Popconfirm>
  )
}

/** The same rules for a dialog confirmation (antd's modal.confirm from App.useApp()). */
export function confirmAction(
  modal: Pick<HookAPI, 'confirm'>,
  { title, okText, danger = true, irreversible, description, ...rest }: Rules & Omit<ModalFuncProps, 'title' | 'okText' | 'content'>,
  t: (key: string) => string,
) {
  const content = description || irreversible ? (
    <>
      {description}
      {description && irreversible ? ' ' : null}
      {irreversible ? t('confirm.irreversible') : null}
    </>
  ) : undefined
  return modal.confirm({
    ...rest,
    title,
    content,
    okText,
    cancelText: t('common.cancel'),
    okButtonProps: { danger },
    autoFocusButton: 'cancel',
  })
}
