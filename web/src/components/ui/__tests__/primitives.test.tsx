import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { LensMark } from '@/components/brand/LensMark'
import { Button } from '../Button'
import { ConfirmDialog } from '../ConfirmDialog'
import { Dialog } from '../Dialog'
import { EmptyState } from '../EmptyState'
import { ErrorBanner } from '../ErrorBanner'
import { Input } from '../Input'
import { Pagination } from '../Pagination'
import { Select } from '../Select'
import { StatusBadge } from '../StatusBadge'
import { Textarea } from '../Textarea'

describe('LensMark', () => {
  it('exposes a name when used as a standalone graphic', () => {
    render(<LensMark label="ProjectLens" width={24} />)
    const mark = screen.getByRole('img', { name: 'ProjectLens' })
    expect(mark).toHaveAttribute('viewBox', '0 0 64 64')
    expect(mark).toHaveAttribute('width', '24')
  })

  it('stays out of the accessibility tree when used with a wordmark', () => {
    const { container } = render(<LensMark />)
    expect(screen.queryByRole('img')).toBeNull()
    expect(container.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
  })
})

describe('Button', () => {
  it('activates from the keyboard without submitting its enclosing form', async () => {
    const user = userEvent.setup()
    const onClick = vi.fn()
    const onSubmit = vi.fn((event) => event.preventDefault())
    render(<form onSubmit={onSubmit}><Button onClick={onClick} size="sm" variant="secondary">Refresh</Button></form>)

    const button = screen.getByRole('button', { name: 'Refresh' })
    expect(button).toHaveClass('button--sm', 'button--secondary')
    await user.tab()
    await user.keyboard('{Enter}')
    expect(onClick).toHaveBeenCalledOnce()
    expect(onSubmit).not.toHaveBeenCalled()
  })
})

describe('Form controls', () => {
  it('associates labels and validation messages with all controls', () => {
    render(
      <>
        <p id="title-hint">Use a short title.</p>
        <Input id="title" label="Title" aria-describedby="title-hint" error="Enter a title." />
        <Textarea label="Content" error="Shorten the content." />
        <Select label="Scope" error="Select a scope."><option value="">Choose</option></Select>
      </>,
    )

    const title = screen.getByRole('textbox', { name: 'Title' })
    const content = screen.getByRole('textbox', { name: 'Content' })
    const scope = screen.getByRole('combobox', { name: 'Scope' })
    expect(title).toHaveAccessibleDescription('Use a short title. Enter a title.')
    expect(content).toHaveAccessibleDescription('Shorten the content.')
    expect(scope).toHaveAccessibleDescription('Select a scope.')
    for (const control of [title, content, scope]) {
      expect(control).toBeInvalid()
    }
  })
})

it('uses neutral badges for repository taxonomy and a warning tone for running jobs', () => {
  render(<><StatusBadge status="local_git" /><StatusBadge status="primary" /><StatusBadge status="running" /></>)
  expect(screen.getByText('Local Git')).toHaveClass('status-badge--neutral')
  expect(screen.getByText('Primary')).toHaveClass('status-badge--neutral')
  expect(screen.getByText('Running')).toHaveClass('status-badge--warning')
})

it('gives the empty state a clear next action', () => {
  render(<EmptyState title="No repositories yet" description="Associate a repository to inspect its files." action={<Button>Associate repository</Button>} />)
  expect(screen.getByRole('heading', { name: 'No repositories yet' })).toBeVisible()
  expect(screen.getByRole('button', { name: 'Associate repository' })).toBeVisible()
})

it('shows structured error details with a recovery action', async () => {
  const user = userEvent.setup()
  const retry = vi.fn()
  render(<ErrorBanner message="Could not load projects." code="internal_error" requestId="request-123" action={<Button onClick={retry}>Retry</Button>} />)
  const alert = screen.getByRole('alert')
  expect(within(alert).getByText('Could not load projects.')).toBeVisible()
  expect(within(alert).getByText('internal_error')).toBeVisible()
  expect(within(alert).getByText('request-123')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(retry).toHaveBeenCalledOnce()
})

describe('Dialogs', () => {
  it('opens a named dialog and closes when the controlled state changes', () => {
    const onDismiss = vi.fn()
    const { rerender } = render(<Dialog open title="Edit settings" onDismiss={onDismiss}>Connection options</Dialog>)
    expect(screen.getByRole('dialog', { name: 'Edit settings' })).toBeVisible()
    rerender(<Dialog open={false} title="Edit settings" onDismiss={onDismiss}>Connection options</Dialog>)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('focuses Cancel and handles native Escape cancellation with no destructive action', () => {
    const onCancel = vi.fn()
    const onConfirm = vi.fn()
    render(
      <ConfirmDialog
        open
        title="Delete project?"
        description="This action is permanent."
        impact={<p>The project and its documents will be deleted.</p>}
        confirmLabel="Delete project"
        onCancel={onCancel}
        onConfirm={onConfirm}
      />,
    )
    const dialog = screen.getByRole('dialog', { name: 'Delete project?' })
    expect(dialog).toHaveAccessibleDescription('This action is permanent.')
    expect(within(dialog).getByText('The project and its documents will be deleted.')).toBeVisible()
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus()

    // Browsers dispatch a cancel event when Escape dismisses a native dialog.
    const cancel = new Event('cancel', { cancelable: true })
    fireEvent(dialog, cancel)
    expect(cancel.defaultPrevented).toBe(true)
    expect(onCancel).toHaveBeenCalledOnce()
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('prevents repeated confirmation while the action is pending', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    const props = { open: true, title: 'Delete project?', confirmLabel: 'Delete project', onConfirm, onCancel: vi.fn() }
    const { rerender } = render(<ConfirmDialog {...props} />)
    const confirm = screen.getByRole('button', { name: 'Delete project' })
    await user.click(confirm)
    expect(onConfirm).toHaveBeenCalledOnce()
    rerender(<ConfirmDialog {...props} pending />)
    expect(confirm).toBeDisabled()
    await user.click(confirm)
    expect(onConfirm).toHaveBeenCalledOnce()
  })
})

describe('Pagination', () => {
  it('uses the known total to cap ranges and disable navigation at the ends', async () => {
    const user = userEvent.setup()
    const onOffsetChange = vi.fn()
    const props = { limit: 10, total: 22, onOffsetChange }
    const { rerender } = render(<Pagination offset={0} {...props} />)
    expect(screen.getByText('1–10 of 22')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(onOffsetChange).toHaveBeenLastCalledWith(10)

    rerender(<Pagination offset={20} {...props} />)
    expect(screen.getByText('21–22 of 22')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Previous' }))
    expect(onOffsetChange).toHaveBeenLastCalledWith(10)
  })

  it('uses an explicit next-page signal when the total is unavailable', () => {
    const props = { offset: 10, limit: 10, itemCount: 3, onOffsetChange: vi.fn() }
    const { rerender } = render(<Pagination {...props} hasMore />)
    expect(screen.getByText('11–13')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Next' })).toBeEnabled()
    rerender(<Pagination {...props} hasMore={false} />)
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
  })
})
