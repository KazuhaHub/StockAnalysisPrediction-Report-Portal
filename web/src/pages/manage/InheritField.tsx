import type { ReactNode } from 'react'
import { Button, Typography } from 'antd'
import { useTranslation } from 'react-i18next'

// One setting that is either inherited or set here.
//
// This replaces a pattern that went wrong in four places at once: a switch labelled "inherit from
// the default group", sitting ABOVE the field it gated, with the field greyed out when it was on.
// All four switches carried the identical label, so none of them said what they inherited or which
// field they belonged to; and a greyed-out field showed no value, so an admin could not see what
// they were inheriting. In one case it was worse than blank — the run-window input kept its
// placeholder "9-18", which is the EXAMPLE from the hint text, so an inheriting field displayed
// something that reads exactly like a value.
//
// Here it is one compact row: the current state says what is effective and where it came from, and
// the action beside it is the only state change available. This follows the same override/inherit
// model as Google Workspace without spending two radio rows on every setting.
export default function InheritField({
  label,
  hint,
  inherited,
  from,
  inheriting,
  onInheritingChange,
  canInherit = true,
  children,
}: {
  label: string
  hint?: ReactNode
  /** The resolved value if inherited, formatted for reading ("unlimited", "9-18", "50"). */
  inherited: string
  /** Where that value comes from — an OU name, or the system. */
  from: string
  inheriting: boolean
  onInheritingChange: (v: boolean) => void
  /**
   * False for a unit that inherits from nobody (the Default OU): there is nothing to offer, so the
   * control stands alone instead of beside a radio whose only option is itself.
   */
  canInherit?: boolean
  /** The override control, rendered only while the override is in effect. */
  children: ReactNode
}) {
  const { t } = useTranslation()
  const inheritedLabel = inherited
    ? t('ou.inheritedAs', { from, value: inherited })
    : t('ou.inheritedFrom', { from })
  if (!canInherit) {
    return (
      <div className="rp-ou-policy-field">
        <Typography.Text strong className="rp-ou-policy-field__label">
          {label}
        </Typography.Text>
        <div className="rp-ou-policy-field__control">{children}</div>
        {hint && (
          <Typography.Text type="secondary" className="rp-ou-policy-field__hint">
            {hint}
          </Typography.Text>
        )}
      </div>
    )
  }
  return (
    <div className="rp-ou-policy-field">
      <Typography.Text strong className="rp-ou-policy-field__label">
        {label}
      </Typography.Text>
      <div className="rp-ou-policy-field__state">
        {inheriting ? (
          <>
            <Typography.Text type="secondary">{inheritedLabel}</Typography.Text>
            <Button type="link" size="small" onClick={() => onInheritingChange(false)}>
              {t('ou.override')}
            </Button>
          </>
        ) : (
          <>
            <div className="rp-ou-policy-field__control">{children}</div>
            <Button type="link" size="small" onClick={() => onInheritingChange(true)}>
              {inheritedLabel}
            </Button>
          </>
        )}
      </div>
      {hint && (
        <Typography.Text type="secondary" className="rp-ou-policy-field__hint">
          {hint}
        </Typography.Text>
      )}
    </div>
  )
}
