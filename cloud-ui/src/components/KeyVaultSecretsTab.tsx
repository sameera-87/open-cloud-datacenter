import {
  Body1,
  Body2,
  Button,
  Card,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Field,
  Input,
  Menu,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Textarea,
  Toast,
  ToastTitle,
  Toaster,
  makeStyles,
  shorthands,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  Add20Regular,
  ArrowClockwise20Regular,
  ArrowUndo20Regular,
  Copy20Regular,
  Delete20Regular,
  Eye20Regular,
  EyeOff20Regular,
  Key24Regular,
  MoreHorizontal20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from './useConfirmDialog';
import { EmptyState, ErrorState, LoadingState } from './list/PageStates';
import { useListPageStyles } from './list/useListPageStyles';
import { fmtDate } from '../lib/date';

interface SecretSummary {
  name: string;
  latest_version: number;
  created_at: string;
  updated_at: string;
  deleted_at?: string;
}

interface SecretValue {
  key: string;
  value: string;
  version: number;
  metadata?: Record<string, string>;
  created_at: string;
  deleted_at?: string;
}

const useStyles = makeStyles({
  tabCard: { padding: tokens.spacingHorizontalL, marginTop: tokens.spacingVerticalM },
  monoName: { fontFamily: tokens.fontFamilyMonospace, fontWeight: 600 },
  cmdBar: {
    display: 'flex',
    gap: tokens.spacingHorizontalS,
    marginBottom: tokens.spacingVerticalM,
  },
  deletedPill: {
    display: 'inline-block',
    padding: `2px ${tokens.spacingHorizontalS}`,
    borderRadius: tokens.borderRadiusCircular,
    fontSize: tokens.fontSizeBase100,
    fontWeight: tokens.fontWeightSemibold,
    backgroundColor: tokens.colorPaletteYellowBackground1,
    color: tokens.colorPaletteDarkOrangeForeground1,
    marginLeft: tokens.spacingHorizontalS,
  },
  dialogForm: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  valueBlock: {
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
    backgroundColor: tokens.colorNeutralBackground3,
    ...shorthands.border('1px', 'solid', tokens.colorNeutralStroke2),
    borderRadius: tokens.borderRadiusMedium,
    padding: tokens.spacingHorizontalM,
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-all',
    maxHeight: '240px',
    overflowY: 'auto',
  },
  metaTable: {
    display: 'grid',
    gridTemplateColumns: 'auto 1fr',
    gap: tokens.spacingHorizontalM,
    rowGap: tokens.spacingVerticalXS,
  },
  metaKey: { color: tokens.colorNeutralForeground3, fontFamily: tokens.fontFamilyMonospace },
  metaValue: { fontFamily: tokens.fontFamilyMonospace },
});

interface Props {
  vaultId: string;
}

const SECRET_NAME_RE = /^[a-z0-9._-]{1,256}$/;

export default function KeyVaultSecretsTab({ vaultId }: Props) {
  const styles = useStyles();
  const listStyles = useListPageStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const confirmDialog = useConfirmDialog();

  const [addOpen, setAddOpen] = useState(false);
  const [addName, setAddName] = useState('');
  const [addValue, setAddValue] = useState('');

  const [revealKey, setRevealKey] = useState<string | null>(null);
  const [revealVersion, setRevealVersion] = useState<number | undefined>(undefined);
  const [revealed, setRevealed] = useState<SecretValue | null>(null);
  // When the latest version is soft-deleted, dc-api returns 410. We capture
  // that here so the modal can show the version-picker UI instead of an error.
  const [softDeleted, setSoftDeleted] = useState(false);
  // Shoulder-surfing defence: secret value is masked by default. User
  // explicitly clicks "Show" to reveal. Copy still works without revealing.
  const [valueShown, setValueShown] = useState(false);

  const secretsQuery = useQuery({
    queryKey: ['keyvault-secrets', tenantId, projectId, vaultId],
    enabled: Boolean(tenantId) && Boolean(projectId) && Boolean(vaultId),
    queryFn: async () => {
      const all: SecretSummary[] = [];
      let cursor: string | undefined;
      for (let i = 0; i < 50; i++) {
        const { data, error } = await api.GET(
          '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}/secrets',
          {
            params: {
              path: { tenant_id: tenantId!, project_id: projectId!, id: vaultId },
              query: cursor ? { cursor, limit: 100 } : { limit: 100 },
            },
          },
        );
        if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
        const page = data as { items?: SecretSummary[]; next_cursor?: string };
        all.push(...(page.items ?? []));
        if (!page.next_cursor) break;
        cursor = page.next_cursor;
      }
      return all;
    },
  });

  const putMutation = useMutation({
    mutationFn: async ({ name, value }: { name: string; value: string }) => {
      const { error } = await api.PUT(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}/secrets/{key}',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId!, id: vaultId, key: name } },
          body: { value } as never,
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: (_data, vars) => {
      dispatchToast(
        <Toast><ToastTitle>Saved secret "{vars.name}"</ToastTitle></Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['keyvault-secrets', tenantId, projectId, vaultId] });
      setAddOpen(false);
      setAddName('');
      setAddValue('');
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Save failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const revealMutation = useMutation({
    mutationFn: async ({ key, version }: { key: string; version?: number }) => {
      const { data, error, response } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}/secrets/{key}',
        {
          params: {
            path: { tenant_id: tenantId!, project_id: projectId!, id: vaultId, key },
            query: version ? { version } : undefined,
          },
        },
      );
      if (error) {
        // 410 Gone = latest version is soft-deleted. Don't bubble — let the
        // modal render the version-picker UI for prior versions.
        if (response?.status === 410) {
          return { __softDeleted: true } as const;
        }
        throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      }
      return data as SecretValue;
    },
    onSuccess: (v) => {
      if ('__softDeleted' in v) {
        setSoftDeleted(true);
        setRevealed(null);
      } else {
        setSoftDeleted(false);
        setRevealed(v);
      }
    },
    onError: (e: Error) => {
      setRevealKey(null);
      dispatchToast(<Toast><ToastTitle>{e.message}</ToastTitle></Toast>, { intent: 'warning' });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (key: string) => {
      const { error } = await api.DELETE(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}/secrets/{key}',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, id: vaultId, key } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Secret soft-deleted</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['keyvault-secrets', tenantId, projectId, vaultId] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const restoreMutation = useMutation({
    mutationFn: async (key: string) => {
      const { error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}/secrets/{key}/restore',
        { params: { path: { tenant_id: tenantId!, project_id: projectId!, id: vaultId, key } } },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: (_d, key) => {
      dispatchToast(
        <Toast><ToastTitle>Restored "{key}" — latest version is readable again</ToastTitle></Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['keyvault-secrets', tenantId, projectId, vaultId] });
      // If the user clicked Restore from inside the reveal dialog, refetch
      // the value so it shows up where the "soft-deleted" panel was.
      if (revealKey) {
        setSoftDeleted(false);
        revealMutation.mutate({ key: revealKey });
      }
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Restore failed: {e.message}</ToastTitle></Toast>, { intent: 'warning' });
    },
  });

  const openReveal = (key: string) => {
    setRevealKey(key);
    setRevealed(null);
    setSoftDeleted(false);
    setRevealVersion(undefined);
    setValueShown(false);
    revealMutation.mutate({ key });
  };

  const fetchVersion = (n: number) => {
    if (!revealKey) return;
    setRevealVersion(n);
    setRevealed(null);
    setValueShown(false);
    revealMutation.mutate({ key: revealKey, version: n });
  };

  const closeReveal = () => {
    setRevealKey(null);
    setRevealed(null);
    setSoftDeleted(false);
    setRevealVersion(undefined);
    setValueShown(false);
  };

  const onDelete = async (key: string) => {
    const ok = await confirmDialog({
      title: `Soft-delete secret "${key}"?`,
      body: 'The latest version is soft-deleted. Prior versions remain readable by version number until the vault\'s soft-delete window elapses.',
      confirmLabel: 'Delete',
      destructive: true,
    });
    if (!ok) return;
    deleteMutation.mutate(key);
  };

  const onCopyValue = (val: string) => {
    void navigator.clipboard.writeText(val);
    dispatchToast(<Toast><ToastTitle>Copied to clipboard</ToastTitle></Toast>, { intent: 'success' });
  };

  const addNameValid = SECRET_NAME_RE.test(addName);
  const secrets = secretsQuery.data ?? [];

  return (
    <Card className={styles.tabCard}>
      <Toaster toasterId={toasterId} />

      <div className={styles.cmdBar}>
        <Button
          appearance="primary"
          icon={<Add20Regular />}
          onClick={() => setAddOpen(true)}
        >
          Add secret
        </Button>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => secretsQuery.refetch()}
          disabled={secretsQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {secretsQuery.isLoading && <LoadingState label="Loading secrets…" />}

      {secretsQuery.isError && !secretsQuery.isLoading && (
        <ErrorState
          message={`Failed to load secrets: ${(secretsQuery.error as Error).message}`}
        />
      )}

      {!secretsQuery.isLoading && !secretsQuery.isError && secrets.length === 0 && (
        <EmptyState
          icon={<Key24Regular />}
          title="No secrets in this key vault yet"
          description="Add a secret to make it available to workloads that hold this vault's credentials."
          action={
            <Button appearance="primary" icon={<Add20Regular />} onClick={() => setAddOpen(true)}>
              Add secret
            </Button>
          }
        />
      )}

      {!secretsQuery.isLoading && !secretsQuery.isError && secrets.length > 0 && (
        <Table size="small" aria-label="Secrets">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Name</TableHeaderCell>
              <TableHeaderCell>Latest version</TableHeaderCell>
              <TableHeaderCell>Created</TableHeaderCell>
              <TableHeaderCell>Updated</TableHeaderCell>
              <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {secrets.map((s) => (
              <TableRow key={s.name}>
                <TableCell>
                  <span className={styles.monoName}>{s.name}</span>
                  {s.deleted_at && <span className={styles.deletedPill}>soft-deleted</span>}
                </TableCell>
                <TableCell className={listStyles.tableMutedCell}>v{s.latest_version}</TableCell>
                <TableCell className={listStyles.tableMutedCell}>{fmtDate(s.created_at)}</TableCell>
                <TableCell className={listStyles.tableMutedCell}>{fmtDate(s.updated_at)}</TableCell>
                <TableCell>
                  <Menu>
                    <MenuTrigger disableButtonEnhancement>
                      <Button appearance="subtle" icon={<MoreHorizontal20Regular />} aria-label="Actions" />
                    </MenuTrigger>
                    <MenuPopover>
                      <MenuList>
                        <MenuItem icon={<Eye20Regular />} onClick={() => openReveal(s.name)}>
                          View value
                        </MenuItem>
                        {s.deleted_at ? (
                          <MenuItem
                            icon={<ArrowUndo20Regular />}
                            onClick={() => restoreMutation.mutate(s.name)}
                            disabled={restoreMutation.isPending}
                          >
                            Restore
                          </MenuItem>
                        ) : (
                          <MenuItem
                            icon={<Delete20Regular />}
                            onClick={() => onDelete(s.name)}
                            disabled={deleteMutation.isPending}
                          >
                            Soft-delete
                          </MenuItem>
                        )}
                      </MenuList>
                    </MenuPopover>
                  </Menu>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {!secretsQuery.isLoading && secrets.length > 0 && (
        <Body1 style={{ color: tokens.colorNeutralForeground3, marginTop: tokens.spacingVerticalM }}>
          {secrets.length} secret{secrets.length === 1 ? '' : 's'}
        </Body1>
      )}

      {/* ── Add-secret dialog ──────────────────────────────────────────── */}
      <Dialog open={addOpen} onOpenChange={(_, d) => setAddOpen(d.open)}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Add secret</DialogTitle>
            <DialogContent>
              <div className={styles.dialogForm}>
                <Body1 style={{ color: tokens.colorNeutralForeground3 }}>
                  If a secret with this name already exists, a new version is written
                  (the prior versions remain readable by version number).
                </Body1>
                <Field
                  label="Name"
                  required
                  hint="Lowercase, . - _ allowed. 1–256 chars. Matches Azure KV naming."
                  validationState={addName && !addNameValid ? 'error' : 'none'}
                  validationMessage={
                    addName && !addNameValid ? 'Invalid name — use [a-z0-9._-]{1,256}.' : undefined
                  }
                >
                  <Input
                    value={addName}
                    onChange={(_, d) => setAddName(d.value.toLowerCase().replace(/[^a-z0-9._-]/g, ''))}
                    placeholder="e.g. db-password"
                  />
                </Field>
                <Field
                  label="Value"
                  required
                  hint="The plaintext secret value. Up to 1 MiB."
                >
                  <Textarea
                    value={addValue}
                    onChange={(_, d) => setAddValue(d.value)}
                    rows={5}
                    placeholder="hunter2"
                  />
                </Field>
              </div>
            </DialogContent>
            <DialogActions>
              <Button appearance="subtle" onClick={() => setAddOpen(false)} disabled={putMutation.isPending}>
                Cancel
              </Button>
              <Button
                appearance="primary"
                onClick={() => putMutation.mutate({ name: addName, value: addValue })}
                disabled={!addNameValid || addValue.length === 0 || putMutation.isPending}
              >
                {putMutation.isPending ? 'Saving…' : 'Save'}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>

      {/* ── Reveal-value dialog ────────────────────────────────────────── */}
      <Dialog open={revealKey !== null} onOpenChange={(_, d) => !d.open && closeReveal()}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>{revealKey}</DialogTitle>
            <DialogContent>
              {revealMutation.isPending && <Body1>Fetching…</Body1>}

              {/* Latest version is soft-deleted — no value to show until the
                  user picks a prior version. */}
              {!revealMutation.isPending && softDeleted && !revealed && (
                <div className={styles.dialogForm}>
                  <Body1>
                    This secret's latest version is <strong>soft-deleted</strong>. You can{' '}
                    <strong>Restore</strong> the latest version (it becomes readable again with
                    its original value), or read a prior version by version number.
                  </Body1>
                  <div>
                    <Button
                      appearance="primary"
                      icon={<ArrowUndo20Regular />}
                      onClick={() => revealKey && restoreMutation.mutate(revealKey)}
                      disabled={restoreMutation.isPending}
                    >
                      {restoreMutation.isPending ? 'Restoring…' : 'Restore latest version'}
                    </Button>
                  </div>
                  {(() => {
                    const row = secrets.find((s) => s.name === revealKey);
                    const latest = row?.latest_version ?? 1;
                    const choices = Array.from({ length: latest - 1 }, (_, i) => latest - 1 - i);
                    if (choices.length === 0) {
                      return (
                        <Body2 style={{ color: tokens.colorNeutralForeground3 }}>
                          No prior versions exist. Add a new version with "Add secret" to
                          restore access.
                        </Body2>
                      );
                    }
                    return (
                      <Field label="Pick a prior version">
                        <div style={{ display: 'flex', flexWrap: 'wrap', gap: tokens.spacingHorizontalS }}>
                          {choices.map((v) => (
                            <Button
                              key={v}
                              appearance="secondary"
                              size="small"
                              onClick={() => fetchVersion(v)}
                            >
                              v{v}
                            </Button>
                          ))}
                        </div>
                      </Field>
                    );
                  })()}
                </div>
              )}

              {revealed && (
                <div className={styles.dialogForm}>
                  <div>
                    <Body2 style={{ color: tokens.colorNeutralForeground3 }}>
                      Version {revealed.version} · created {fmtDate(revealed.created_at)}
                      {revealed.deleted_at && ' · soft-deleted ' + fmtDate(revealed.deleted_at)}
                      {revealVersion !== undefined && softDeleted && ' · viewing prior version'}
                    </Body2>
                  </div>
                  <Field label="Value">
                    <div className={styles.valueBlock}>
                      {valueShown ? revealed.value : '••••••••••••'}
                    </div>
                    <div style={{ marginTop: tokens.spacingVerticalS }}>
                      <Button
                        appearance="subtle"
                        size="small"
                        icon={valueShown ? <EyeOff20Regular /> : <Eye20Regular />}
                        onClick={() => setValueShown((s) => !s)}
                      >
                        {valueShown ? 'Hide' : 'Show'}
                      </Button>
                    </div>
                  </Field>
                  {revealed.metadata && Object.keys(revealed.metadata).length > 0 && (
                    <Field label="Metadata">
                      <div className={styles.metaTable}>
                        {Object.entries(revealed.metadata).map(([k, v]) => (
                          <div key={k} style={{ display: 'contents' }}>
                            <span className={styles.metaKey}>{k}</span>
                            <span className={styles.metaValue}>{v}</span>
                          </div>
                        ))}
                      </div>
                    </Field>
                  )}
                </div>
              )}
            </DialogContent>
            <DialogActions>
              {revealed && (
                <Button
                  appearance="primary"
                  icon={<Copy20Regular />}
                  onClick={() => onCopyValue(revealed.value)}
                >
                  Copy value
                </Button>
              )}
              <Button appearance="subtle" onClick={closeReveal}>Close</Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </Card>
  );
}
