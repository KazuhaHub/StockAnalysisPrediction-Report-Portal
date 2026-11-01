import { useEffect, useMemo, useRef, useState, type ClipboardEvent, type KeyboardEvent } from 'react'
import {
  Alert,
  Button,
  Input,
  InputNumber,
  Popover,
  Segmented,
  Select,
  Space,
  Tag,
  Tooltip,
  Typography,
  Upload,
} from 'antd'
import {
  DeleteOutlined,
  DownloadOutlined,
  QuestionCircleOutlined,
  UndoOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import Confirm from './Confirm'

import type { PluginInput } from '../api/types'
import {
  activeBatchRows,
  batchRowHasValues,
  blankBatchRow,
  ensureTrailingBlank,
  missingCSVHeaders,
  missingRequiredCells,
  pasteBatchGrid,
  type BatchDraftRow,
} from '../lib/batchRows'
import { csvToRows, downloadCSV, toCSV } from '../lib/csv'
import { inputDisplayMeta } from '../lib/difyInputs'
import { fillScrollWidth, useColumnWidths } from '../lib/columnWidths'
import CardTable, { type CardColumn } from './CardTable'
import TableColumnHeader from './TableColumnHeader'

type EditorMode = 'grid' | 'csv'
type TableRow = { values: BatchDraftRow; rowIndex: number }

// Input columns share the available width but never get narrower than this; when their minimums do
// not fit, the grid scrolls inside itself (docs/frontend-guidelines.md, "Editable grids").
const MIN_COLUMN_WIDTH = 160
const ROW_NUMBER_WIDTH = 54
const DELETE_WIDTH = 62
const PAGE_SIZE = 20
const lastPage = (count: number, size: number) => Math.max(1, Math.ceil(count / size))

export default function BatchRowsEditor({
  inputs,
  rows,
  onChange,
  onValidityChange,
  widthsKey,
}: {
  inputs: PluginInput[]
  rows: BatchDraftRow[]
  onChange: (rows: BatchDraftRow[]) => void
  onValidityChange?: (valid: boolean) => void
  /** Names where dragged column widths are kept (one per workflow); without it they last until unmount. */
  widthsKey?: string
}) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<EditorMode>('grid')
  const [rawCSV, setRawCSV] = useState(() => toCSV(inputs.map((input) => input.key), []))

  const keys = useMemo(() => inputs.map((input) => input.key), [inputs])
  // The grid always ends on one blank row, spreadsheet-style: typing into it makes the next one
  // appear. The blank rows are a view concern: they are never counted, never submitted (activeBatchRows
  // drops them) and never reach the CSV.
  const displayedRows = useMemo(() => ensureTrailingBlank(rows, inputs), [rows, inputs])
  const { widths, resize, cancel, reset: resetWidths, measure } = useColumnWidths(widthsKey, MIN_COLUMN_WIDTH)
  const [page, setPage] = useState({ current: 1, size: PAGE_SIZE })
  const previousCount = useRef(displayedRows.length)
  useEffect(() => {
    const before = previousCount.current
    previousCount.current = displayedRows.length
    // A paste on the last page follows the rows it added onto the next one. Typing grows the grid by
    // exactly one row (the new blank one), and the cell being typed in must stay on screen; a paste
    // into the middle of a long grid does not yank anyone to the end either.
    setPage((p) => {
      const last = lastPage(displayedRows.length, p.size)
      if (displayedRows.length > before + 1 && p.current >= lastPage(before, p.size)) return { ...p, current: last }
      return p.current > last ? { ...p, current: last } : p
    })
  }, [displayedRows.length])
  const readyRows = useMemo(() => activeBatchRows(rows), [rows])
  const missingCells = useMemo(() => missingRequiredCells(rows, inputs), [rows, inputs])
  const missingCellSet = useMemo(
    () => new Set(missingCells.map((cell) => `${cell.rowIndex}:${cell.key}`)),
    [missingCells],
  )
  const headerMissing = useMemo(
    () => (mode === 'csv' ? missingCSVHeaders(rawCSV, inputs) : []),
    [mode, rawCSV, inputs],
  )
  const valid = readyRows.length > 0 && missingCells.length === 0 && headerMissing.length === 0

  useEffect(() => {
    onValidityChange?.(valid)
  }, [onValidityChange, valid])

  const setModeAndSync = (next: EditorMode) => {
    if (next === mode) return
    if (next === 'csv') {
      setRawCSV(toCSV(keys, readyRows.map((row) => keys.map((key) => row[key] ?? ''))))
    } else {
      const parsed = csvToRows(rawCSV, keys)
      onChange(ensureTrailingBlank(parsed, inputs))
    }
    setMode(next)
  }

  const updateCell = (rowIndex: number, key: string, value: string) => {
    const next = displayedRows.map((row) => ({ ...row }))
    next[rowIndex][key] = value
    onChange(ensureTrailingBlank(next, inputs))
  }

  const handlePaste = (event: ClipboardEvent<HTMLElement>, rowIndex: number, columnIndex: number) => {
    const text = event.clipboardData.getData('text')
    if (!text.includes('\n') && !text.includes('\t')) return
    event.preventDefault()
    onChange(ensureTrailingBlank(pasteBatchGrid(displayedRows, inputs, rowIndex, columnIndex, text), inputs))
  }

  const removeRow = (rowIndex: number) => {
    const next = displayedRows.filter((_row, index) => index !== rowIndex)
    onChange(ensureTrailingBlank(next, inputs))
  }

  const importCSV = (file: File) => {
    const reader = new FileReader()
    reader.onload = () => {
      const text = String(reader.result || '')
      setRawCSV(text)
      onChange(csvToRows(text, keys))
      setMode('csv')
    }
    reader.readAsText(file)
    return false
  }

  const currentCSV = () =>
    mode === 'csv' ? rawCSV : toCSV(keys, readyRows.map((row) => keys.map((key) => row[key] ?? '')))

  const fieldControl = (input: PluginInput, rowIndex: number, columnIndex: number) => {
    const { title } = inputDisplayMeta(input)
    const value = displayedRows[rowIndex]?.[input.key] ?? ''
    const status = missingCellSet.has(`${rowIndex}:${input.key}`) ? 'error' : undefined
    const label = t('batch.editor.cellLabel', { row: rowIndex + 1, field: title })
    const common = {
      value,
      status: status as 'error' | undefined,
      'aria-label': label,
      onPaste: (event: ClipboardEvent<HTMLElement>) => handlePaste(event, rowIndex, columnIndex),
    }

    if (input.type === 'select' && input.options?.length) {
      return (
        <Select
          {...common}
          allowClear
          showSearch
          options={input.options.map((option) => ({ value: option, label: option }))}
          onChange={(next) => updateCell(rowIndex, input.key, String(next ?? ''))}
        />
      )
    }
    if (input.type === 'number') {
      return (
        <InputNumber
          {...common}
          stringMode
          controls={false}
          onChange={(next) => updateCell(rowIndex, input.key, String(next ?? ''))}
        />
      )
    }
    // Long text wraps inside the cell: the editor grows up to three lines and then scrolls, instead of
    // widening the column. Every text input uses this editor, but only a paragraph input may hold a
    // line break, so Enter in the others is swallowed rather than leaving a newline inside a value.
    const singleLine = input.type !== 'paragraph'
    return (
      <Input.TextArea
        {...common}
        autoSize={{ minRows: 1, maxRows: 3 }}
        onChange={(event) => updateCell(rowIndex, input.key, event.target.value)}
        onKeyDown={
          singleLine
            ? (event: KeyboardEvent<HTMLTextAreaElement>) => {
                // An input method confirms its candidate with Enter; that keystroke is not ours.
                if (event.key === 'Enter' && !event.nativeEvent.isComposing) event.preventDefault()
              }
            : undefined
        }
      />
    )
  }

  const columns: CardColumn<TableRow>[] = [
    {
      title: '#',
      dataIndex: 'rowIndex',
      key: 'rowIndex',
      width: ROW_NUMBER_WIDTH,
      fixed: 'left',
      align: 'center',
      render: (rowIndex: number) => <Typography.Text type="secondary">{rowIndex + 1}</Typography.Text>,
    },
    ...inputs.map((input, columnIndex) => {
      const { title, detail } = inputDisplayMeta(input)
      return {
        title: (
          <TableColumnHeader id={input.key} name={title} onMeasure={measure} onResize={resize} onCancel={cancel}>
          <div className="rp-batch-grid-heading">
            <div className="rp-batch-grid-heading__title">
              <Tooltip title={title}>
                <span className="rp-batch-grid-heading__name">{title}</span>
              </Tooltip>
              {input.required ? <span className="rp-batch-grid-heading__required">*</span> : <span className="rp-batch-grid-heading__optional">{t('run.optionalMark')}</span>}
              {detail && (
                <Popover title={title} content={<div className="rp-run-input-help__content">{detail}</div>} trigger="click">
                  <Button
                    type="text"
                    size="small"
                    shape="circle"
                    className="rp-batch-grid-heading__help"
                    icon={<QuestionCircleOutlined />}
                    aria-label={t('run.inputHelp', { field: title })}
                  />
                </Popover>
              )}
            </div>
            {title !== input.key && <Typography.Text code>{input.key}</Typography.Text>}
          </div>
          </TableColumnHeader>
        ),
        phoneLabel: title,
        key: input.key,
        // No width until the user drags one: the column then shares what is left with its siblings.
        width: widths[input.key],
        render: (_value: string, record: TableRow) => fieldControl(input, record.rowIndex, columnIndex),
      }
    }),
    // Once every input column has a width the user gave it, the spare space goes to an empty column
    // here rather than being spread back over theirs, so a dragged width is the width shown.
    ...(inputs.length > 0 && inputs.every((input) => widths[input.key] != null)
      ? [{ key: 'rp-grid-filler', onHeaderCell: () => ({ className: 'rp-grid-filler' }), onCell: () => ({ className: 'rp-grid-filler' }) } as CardColumn<TableRow>]
      : []),
    {
      key: 'actions',
      width: DELETE_WIDTH,
      fixed: 'right',
      align: 'center',
      // The trailing blank row is the place to type the next row, not a row to delete.
      render: (_value, record) =>
        record.rowIndex === displayedRows.length - 1 && !batchRowHasValues(record.values) ? null : (
          <Tooltip title={t('batch.editor.deleteRow')}>
            <Button
              type="text"
              danger
              icon={<DeleteOutlined />}
              aria-label={t('batch.editor.deleteRowN', { row: record.rowIndex + 1 })}
              onClick={() => removeRow(record.rowIndex)}
            />
          </Tooltip>
        ),
    },
  ]

  const tableRows: TableRow[] = displayedRows.map((values, rowIndex) => ({ values, rowIndex }))
  const missingRowCount = new Set(missingCells.map((cell) => cell.rowIndex)).size

  return (
    <div className="rp-batch-rows-editor">
      <div className="rp-batch-editor-toolbar">
        <Space wrap size={8}>
          <Segmented
            value={mode}
            onChange={(value) => setModeAndSync(value as EditorMode)}
            options={[
              { value: 'grid', label: t('batch.editor.gridMode') },
              { value: 'csv', label: t('batch.editor.csvMode') },
            ]}
          />
          <Tag color={readyRows.length > 0 ? 'blue' : undefined}>{t('batch.parsedRows', { n: readyRows.length })}</Tag>
        </Space>
        <Space wrap size={4}>
          {mode === 'grid' && Object.keys(widths).length > 0 && (
            <Button size="small" type="text" icon={<UndoOutlined />} onClick={resetWidths}>
              {t('batch.editor.resetWidths')}
            </Button>
          )}
          <Upload accept=".csv,.txt" showUploadList={false} beforeUpload={importCSV}>
            <Button size="small" icon={<UploadOutlined />}>{t('batch.uploadCsv')}</Button>
          </Upload>
          <Button size="small" icon={<DownloadOutlined />} onClick={() => downloadCSV('batch-rows.csv', currentCSV())}>
            {t('batch.editor.downloadRows')}
          </Button>
          <Confirm
            title={t('batch.editor.clearConfirm')}
            onConfirm={() => {
              setRawCSV(toCSV(keys, []))
              onChange([blankBatchRow(inputs)])
            }}
            okText={t('batch.editor.clearAction')}
          >
            <Button size="small" danger>{t('batch.editor.clear')}</Button>
          </Confirm>
        </Space>
      </div>

      {mode === 'grid' ? (
        <>
          <Typography.Paragraph type="secondary" className="rp-batch-editor-hint">
            {t('batch.editor.pasteHint')}
          </Typography.Paragraph>
          <div className="rp-batch-grid">
            <CardTable<TableRow>
              size="small"
              className="rp-adaptive-table"
              rowKey={(record) => String(record.rowIndex)}
              columns={columns}
              dataSource={tableRows}
              pagination={
                tableRows.length > page.size
                  ? {
                      current: page.current,
                      pageSize: page.size,
                      showSizeChanger: true,
                      onChange: (current, size) => setPage({ current, size }),
                    }
                  : false
              }
              scroll={{
                x: fillScrollWidth(keys, widths, MIN_COLUMN_WIDTH, ROW_NUMBER_WIDTH + DELETE_WIDTH),
                y: 420,
              }}
            />
          </div>
        </>
      ) : (
        <Input.TextArea
          className="rp-batch-csv-editor"
          rows={12}
          value={rawCSV}
          onChange={(event) => {
            const text = event.target.value
            setRawCSV(text)
            onChange(csvToRows(text, keys))
          }}
          placeholder={t('batch.csvPlaceholder', { keys: keys.join(',') })}
        />
      )}

      {headerMissing.length > 0 ? (
        <Alert type="error" showIcon title={t('batch.editor.missingHeaders', { fields: headerMissing.join(', ') })} />
      ) : missingCells.length > 0 ? (
        <Alert
          type="error"
          showIcon
          title={t('batch.editor.missingRequired', { cells: missingCells.length, rows: missingRowCount })}
        />
      ) : readyRows.length > 0 ? (
        <Alert type="success" showIcon title={t('batch.editor.ready', { n: readyRows.length })} />
      ) : (
        <Alert type="info" showIcon title={t('batch.editor.empty')} />
      )}
    </div>
  )
}
