import { DatePicker, Input, InputNumber, Select } from 'antd'
import dayjs from 'dayjs'
import type { ExportField, ExportOperator } from '../../../api/types'
import type { ExportLabels } from './labels'

const DATE = 'YYYY-MM-DD'

// The value editor for one condition. Its shape follows the field's type and the operator's arity,
// and it is shared by the chip popover and the advanced builder rows so both behave the same.
export default function ValueInput({
  field,
  operator,
  values,
  onChange,
  labels,
  placeholder,
  ariaLabel,
}: {
  field: ExportField
  operator: ExportOperator
  values: string[]
  onChange: (values: string[]) => void
  labels: ExportLabels
  placeholder?: string
  ariaLabel?: string
}) {
  const full = { width: '100%', minWidth: 160 }
  const multi = operator === 'in' || operator === 'not_in'

  if (field.type === 'enum') {
    const options = (field.options ?? []).map((v) => ({ value: v, label: labels.value(field.key, v) }))
    // Search the shown label and the stored identifier, so "report.read" finds "read a report".
    const filterOption = (input: string, o?: { value?: unknown; label?: unknown }) =>
      `${String(o?.label ?? '')} ${String(o?.value ?? '')}`.toLowerCase().includes(input.toLowerCase())
    if (multi) {
      return (
        <Select
          mode={options.length > 0 ? 'multiple' : 'tags'}
          showSearch
          allowClear
          style={full}
          aria-label={ariaLabel}
          placeholder={placeholder}
          value={values}
          options={options}
          filterOption={filterOption}
          maxTagCount="responsive"
          onChange={(v: string[]) => onChange(v)}
        />
      )
    }
    return (
      <Select
        showSearch
        allowClear
        style={full}
        aria-label={ariaLabel}
        placeholder={placeholder}
        value={values[0]}
        options={options}
        filterOption={filterOption}
        onChange={(v?: string) => onChange(v ? [v] : [])}
      />
    )
  }

  if (field.type === 'date' || field.type === 'datetime') {
    if (operator === 'between') {
      return (
        <DatePicker.RangePicker
          style={{ width: '100%', minWidth: 240 }}
          aria-label={ariaLabel}
          value={values.length === 2 ? [dayjs(values[0]), dayjs(values[1])] : null}
          onChange={(r) => onChange(r?.[0] && r[1] ? [r[0].format(DATE), r[1].format(DATE)] : [])}
        />
      )
    }
    return (
      <DatePicker
        style={full}
        aria-label={ariaLabel}
        value={values[0] ? dayjs(values[0]) : null}
        onChange={(d) => onChange(d ? [d.format(DATE)] : [])}
      />
    )
  }

  if (field.type === 'number') {
    return (
      <InputNumber
        style={full}
        aria-label={ariaLabel}
        placeholder={placeholder}
        value={values[0] === undefined || values[0] === '' ? null : Number(values[0])}
        onChange={(n) => onChange(n === null ? [] : [String(n)])}
      />
    )
  }

  return (
    <Input
      allowClear
      style={full}
      aria-label={ariaLabel}
      placeholder={placeholder}
      value={values[0] ?? ''}
      onChange={(e) => onChange(e.target.value ? [e.target.value] : [])}
    />
  )
}
