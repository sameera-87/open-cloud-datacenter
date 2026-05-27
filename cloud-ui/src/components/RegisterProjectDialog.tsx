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
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { useApi } from '../api/useApi';

const PROJECT_ID_RE = /^[a-z][a-z0-9-]{0,18}[a-z0-9]$/;

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
  quotaRow: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr 1fr',
    gap: tokens.spacingHorizontalM,
  },
  capCard: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalXS,
    padding: tokens.spacingHorizontalM,
    backgroundColor: tokens.colorNeutralBackground2,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
  },
  capRow: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr 1fr',
    gap: tokens.spacingHorizontalM,
    fontSize: tokens.fontSizeBase200,
  },
  capLabel: {
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase100,
    textTransform: 'uppercase',
    letterSpacing: '0.04em',
  },
  capValueOK: {
    fontFamily: tokens.fontFamilyMonospace,
    fontWeight: tokens.fontWeightSemibold,
  },
  capValueOver: {
    fontFamily: tokens.fontFamilyMonospace,
    fontWeight: tokens.fontWeightSemibold,
    color: tokens.colorPaletteRedForeground1,
  },
  capHeading: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
    marginBottom: tokens.spacingVerticalXS,
  },
});

interface RegisterProjectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tenantId: string;
  onCreated?: (projectId: string) => void;
}

// Raw shape that the dc-api 400 quota_exceeded body carries. Locally-typed
// (not via openapi-fetch) because openapi-fetch's `error` is a union that's
// hard to narrow inline.
type QuotaExceededBody = {
  error?: 'quota_exceeded';
  message?: string;
  tenant_cap?: { cpu_cores: number; memory_gb: number; storage_gb: number };
  allocated?: { cpu_cores: number; memory_gb: number; storage_gb: number };
  available?: { cpu_cores: number; memory_gb: number; storage_gb: number };
  requested?: { cpu_cores: number; memory_gb: number; storage_gb: number };
};

export default function RegisterProjectDialog({
  open,
  onOpenChange,
  tenantId,
  onCreated,
}: RegisterProjectDialogProps) {
  const styles = useStyles();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const [projectId, setProjectId] = useState('');
  const [projectName, setProjectName] = useState('');
  const [projectDesc, setProjectDesc] = useState('');
  const [cpuCores, setCpuCores] = useState('20');
  const [memoryGb, setMemoryGb] = useState('64');
  const [storageGb, setStorageGb] = useState('500');
  const [submitting, setSubmitting] = useState(false);
  const [idError, setIdError] = useState('');
  const [submitError, setSubmitError] = useState('');

  // Fetch tenant cap/allocation so the dialog can show "X available" inline.
  // Refetched on every open via the `open` flag in the query key, so a user
  // who creates a project, closes, and reopens sees the new available figure.
  const capQuery = useQuery({
    queryKey: ['tenant-cap-usage', tenantId, open],
    enabled: open && Boolean(tenantId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/cap-usage', {
        params: { path: { tenant_id: tenantId } },
      });
      if (error) {
        throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      }
      return data;
    },
  });

  const requestedCpu = Number(cpuCores) || 0;
  const requestedMem = Number(memoryGb) || 0;
  const requestedSto = Number(storageGb) || 0;

  const overCpu = capQuery.data ? requestedCpu > capQuery.data.available.cpu_cores : false;
  const overMem = capQuery.data ? requestedMem > capQuery.data.available.memory_gb : false;
  const overSto = capQuery.data ? requestedSto > capQuery.data.available.storage_gb : false;

  const reset = () => {
    setProjectId('');
    setProjectName('');
    setProjectDesc('');
    setCpuCores('20');
    setMemoryGb('64');
    setStorageGb('500');
    setIdError('');
    setSubmitError('');
  };

  const close = () => {
    if (submitting) return;
    onOpenChange(false);
    setTimeout(reset, 150);
  };

  const validateId = (value: string): string => {
    if (!value.trim()) return 'Project ID is required.';
    if (!PROJECT_ID_RE.test(value.trim())) {
      return 'Must match ^[a-z][a-z0-9-]{0,18}[a-z0-9]$. Example: prod-infra';
    }
    return '';
  };

  const handleRegister = async () => {
    const idErr = validateId(projectId);
    if (idErr) {
      setIdError(idErr);
      return;
    }

    setSubmitting(true);
    setSubmitError('');

    try {
      const body: Record<string, unknown> = {
        id: projectId.trim(),
        cpu_cores: requestedCpu || 20,
        memory_gb: requestedMem || 64,
        storage_gb: requestedSto || 500,
      };
      if (projectName.trim()) body.name = projectName.trim();
      if (projectDesc.trim()) body.description = projectDesc.trim();

      const { response, error } = await api.POST('/v1/tenants/{tenant_id}/projects', {
        params: { path: { tenant_id: tenantId } },
        body: body as never,
      });

      if (response.status === 201) {
        const newId = projectId.trim();
        await queryClient.invalidateQueries({ queryKey: ['projects', tenantId] });
        await queryClient.invalidateQueries({ queryKey: ['tenant-cap-usage', tenantId] });
        onOpenChange(false);
        setTimeout(reset, 150);
        if (onCreated) {
          onCreated(newId);
        } else {
          navigate(`/tenants/${tenantId}/projects/${newId}/dashboard`);
        }
        return;
      }

      if (response.status === 409) {
        setSubmitError('A project with that id already exists in this tenant.');
        return;
      }

      if (response.status === 400) {
        // dc-api uses two distinct 400 shapes for cap-related rejections:
        //   - error=quota_exceeded → tenant cap breach (rich body)
        //   - error=quota_below_usage → in-use guard (only for PATCH; not reachable here)
        // Anything else is generic { error: "..." }.
        const body = error as QuotaExceededBody | { error?: string; message?: string } | undefined;
        if (body && (body as QuotaExceededBody).error === 'quota_exceeded') {
          const qe = body as QuotaExceededBody;
          const parts: string[] = [];
          if (qe.available) {
            parts.push(
              `available: ${qe.available.cpu_cores} cpu / ${qe.available.memory_gb} GiB RAM / ${qe.available.storage_gb} GiB storage`,
            );
          }
          if (qe.requested) {
            parts.push(
              `requested: ${qe.requested.cpu_cores} cpu / ${qe.requested.memory_gb} GiB / ${qe.requested.storage_gb} GiB`,
            );
          }
          setSubmitError(
            (qe.message || 'Quota exceeded.') +
              (parts.length ? '  (' + parts.join(' — ') + ')' : ''),
          );
          // Refresh the cap display so the user sees the up-to-date numbers.
          void capQuery.refetch();
          return;
        }
        const msg =
          body && typeof body === 'object' && 'message' in body && typeof body.message === 'string'
            ? body.message
            : body && typeof body === 'object' && 'error' in body && typeof body.error === 'string'
              ? body.error
              : 'Invalid request.';
        setSubmitError(msg);
        return;
      }

      setSubmitError('Failed to create project. Try again.');
    } catch {
      setSubmitError('Failed to create project. Try again.');
    } finally {
      setSubmitting(false);
    }
  };

  const renderCapCard = () => {
    if (capQuery.isLoading) {
      return (
        <div className={styles.capCard}>
          <Spinner size="extra-tiny" label="Loading tenant capacity…" />
        </div>
      );
    }
    if (capQuery.isError || !capQuery.data) {
      return null; // Don't block the form on a transient lookup failure.
    }
    const u = capQuery.data;
    return (
      <div className={styles.capCard}>
        <span className={styles.capHeading}>Tenant capacity</span>
        <div className={styles.capRow}>
          <span className={styles.capLabel}>CPU cores</span>
          <span className={styles.capLabel}>Memory (GiB)</span>
          <span className={styles.capLabel}>Storage (GiB)</span>
        </div>
        <div className={styles.capRow}>
          <span>{`cap ${u.cap.cpu_cores}`}</span>
          <span>{`cap ${u.cap.memory_gb}`}</span>
          <span>{`cap ${u.cap.storage_gb}`}</span>
        </div>
        <div className={styles.capRow}>
          <span>{`used ${u.allocated.cpu_cores}`}</span>
          <span>{`used ${u.allocated.memory_gb}`}</span>
          <span>{`used ${u.allocated.storage_gb}`}</span>
        </div>
        <div className={styles.capRow}>
          <span className={overCpu ? styles.capValueOver : styles.capValueOK}>
            {`available ${u.available.cpu_cores}`}
          </span>
          <span className={overMem ? styles.capValueOver : styles.capValueOK}>
            {`available ${u.available.memory_gb}`}
          </span>
          <span className={overSto ? styles.capValueOver : styles.capValueOK}>
            {`available ${u.available.storage_gb}`}
          </span>
        </div>
      </div>
    );
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
          <DialogTitle>Create a project</DialogTitle>
          <DialogContent>
            <div className={styles.form}>
              <Field
                label="ID"
                required
                hint="Slug: lowercase letters, digits, hyphens. 2-20 chars. Example: prod-infra"
                validationMessage={idError || undefined}
                validationState={idError ? 'error' : 'none'}
              >
                <Input
                  value={projectId}
                  onChange={(_, d) => {
                    setProjectId(d.value);
                    if (idError) setIdError(validateId(d.value));
                  }}
                  placeholder="prod-infra"
                  disabled={submitting}
                />
              </Field>

              <Field label="Name" hint="Display name. Defaults to id when blank.">
                <Input
                  value={projectName}
                  onChange={(_, d) => setProjectName(d.value)}
                  placeholder="Production Infrastructure"
                  disabled={submitting}
                />
              </Field>

              <Field label="Description" hint="Optional. Free text.">
                <Textarea
                  value={projectDesc}
                  onChange={(_, d) => setProjectDesc(d.value)}
                  placeholder="Core networking and compute for the platform team"
                  disabled={submitting}
                  rows={2}
                />
              </Field>

              {renderCapCard()}

              <div className={styles.quotaRow}>
                <Field
                  label="CPU cores"
                  hint="vCPU quota"
                  validationState={overCpu ? 'error' : 'none'}
                  validationMessage={overCpu ? 'Exceeds available' : undefined}
                >
                  <Input
                    type="number"
                    value={cpuCores}
                    onChange={(_, d) => setCpuCores(d.value)}
                    min={1}
                    disabled={submitting}
                  />
                </Field>
                <Field
                  label="Memory (GB)"
                  hint="RAM quota"
                  validationState={overMem ? 'error' : 'none'}
                  validationMessage={overMem ? 'Exceeds available' : undefined}
                >
                  <Input
                    type="number"
                    value={memoryGb}
                    onChange={(_, d) => setMemoryGb(d.value)}
                    min={1}
                    disabled={submitting}
                  />
                </Field>
                <Field
                  label="Storage (GB)"
                  hint="Disk quota"
                  validationState={overSto ? 'error' : 'none'}
                  validationMessage={overSto ? 'Exceeds available' : undefined}
                >
                  <Input
                    type="number"
                    value={storageGb}
                    onChange={(_, d) => setStorageGb(d.value)}
                    min={1}
                    disabled={submitting}
                  />
                </Field>
              </div>

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
              disabled={submitting || !projectId.trim()}
              icon={submitting ? <Spinner size="tiny" /> : undefined}
            >
              {submitting ? 'Creating…' : 'Create'}
            </Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}
