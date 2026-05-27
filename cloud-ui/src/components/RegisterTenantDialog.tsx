import {
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Field,
  Input,
  Spinner,
  Text,
  Textarea,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { useApi } from '../api/useApi';

/**
 * Reusable "register a tenant" dialog. Opened from NoTenantsPage (first-tenant
 * onboarding), the TenantSwitcher dropdown ("+ Create tenant"), and the
 * TenantPickerPage. Admin-only — the parent should gate visibility.
 *
 * onCreated: optional callback. When omitted, navigates to the new tenant's
 * dashboard. When provided, the caller decides what to do (e.g. just close
 * the dialog and let react-query refresh the list).
 */

const TENANT_ID_RE = /^[a-z][a-z0-9-]{0,30}[a-z0-9]$/;

const useStyles = makeStyles({
  form: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
    paddingTop: tokens.spacingVerticalS,
  },
  error: {
    color: tokens.colorPaletteRedForeground1,
    fontSize: tokens.fontSizeBase200,
  },
});

interface RegisterTenantDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated?: (tenantId: string) => void;
}

export default function RegisterTenantDialog({
  open,
  onOpenChange,
  onCreated,
}: RegisterTenantDialogProps) {
  const styles = useStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const [tenantId, setTenantId] = useState('');
  const [tenantName, setTenantName] = useState('');
  const [tenantDesc, setTenantDesc] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [idError, setIdError] = useState('');
  const [submitError, setSubmitError] = useState('');

  const reset = () => {
    setTenantId('');
    setTenantName('');
    setTenantDesc('');
    setIdError('');
    setSubmitError('');
  };

  const close = () => {
    if (submitting) return;
    onOpenChange(false);
    // Reset state on close so the next open is clean.
    setTimeout(reset, 150);
  };

  const validateId = (value: string): string => {
    if (!value.trim()) return 'Tenant ID is required.';
    if (!TENANT_ID_RE.test(value.trim())) {
      return 'Must match ^[a-z][a-z0-9-]{0,30}[a-z0-9]$. Example: cs-team';
    }
    return '';
  };

  const handleRegister = async () => {
    const idErr = validateId(tenantId);
    if (idErr) {
      setIdError(idErr);
      return;
    }

    setSubmitting(true);
    setSubmitError('');

    try {
      const body: { id: string; name?: string; description?: string } = {
        id: tenantId.trim(),
      };
      if (tenantName.trim()) body.name = tenantName.trim();
      if (tenantDesc.trim()) body.description = tenantDesc.trim();

      const { response, error } = await api.POST('/v1/admin/tenants', {
        body: body as never,
      });

      if (response.status === 201) {
        const newId = body.id;
        // Invalidate tenants cache so every consumer (TenantSwitcher, picker,
        // etc.) refreshes its list. The route navigation below also triggers
        // a refetch via the route-key change, but invalidating is explicit.
        await queryClient.invalidateQueries({ queryKey: ['tenants'] });
        onOpenChange(false);
        setTimeout(reset, 150);
        if (onCreated) {
          onCreated(newId);
        } else {
          navigate(`/tenants/${newId}`);
        }
        return;
      }

      if (response.status === 409) {
        setSubmitError('A tenant with that id already exists.');
        return;
      }

      if (response.status === 400) {
        const msg =
          error && typeof error === 'object' && 'message' in error
            ? String((error as { message: unknown }).message)
            : 'Invalid request.';
        setSubmitError(msg);
        return;
      }

      setSubmitError('Failed to register tenant. Try again.');
    } catch {
      setSubmitError('Failed to register tenant. Try again.');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(_, d) => {
        if (!submitting) onOpenChange(d.open);
      }}
    >
      <DialogSurface>
        <DialogBody>
          <DialogTitle>Register a tenant</DialogTitle>
          <DialogContent>
            <div className={styles.form}>
              <Field
                label="ID"
                required
                hint="Slug: lowercase letters, digits, hyphens. 2–32 chars. Example: cs-team"
                validationMessage={idError || undefined}
                validationState={idError ? 'error' : 'none'}
              >
                <Input
                  value={tenantId}
                  onChange={(_, d) => {
                    setTenantId(d.value);
                    if (idError) setIdError(validateId(d.value));
                  }}
                  placeholder="cs-team"
                  disabled={submitting}
                />
              </Field>

              <Field label="Name" hint="Display name. Defaults to id when blank.">
                <Input
                  value={tenantName}
                  onChange={(_, d) => setTenantName(d.value)}
                  placeholder="Customer Success"
                  disabled={submitting}
                />
              </Field>

              <Field label="Description" hint="Optional. Free text.">
                <Textarea
                  value={tenantDesc}
                  onChange={(_, d) => setTenantDesc(d.value)}
                  placeholder="SRE-managed support tenant"
                  disabled={submitting}
                  rows={3}
                />
              </Field>

              {submitError && <Text className={styles.error}>{submitError}</Text>}
            </div>
          </DialogContent>
          <DialogActions>
            <Button appearance="subtle" disabled={submitting} onClick={close}>
              Cancel
            </Button>
            <Button
              appearance="primary"
              onClick={() => void handleRegister()}
              disabled={submitting || !tenantId.trim()}
              icon={submitting ? <Spinner size="tiny" /> : undefined}
            >
              {submitting ? 'Registering…' : 'Register'}
            </Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}
