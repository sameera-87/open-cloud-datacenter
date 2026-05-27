import {
  Button,
  Card,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  DialogTrigger,
  Dropdown,
  Field,
  Input,
  Menu,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
  Option,
  Subtitle1,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Title2,
  Toast,
  ToastTitle,
  Toaster,
  makeStyles,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  Add20Regular,
  ArrowClockwise20Regular,
  MoreHorizontal20Regular,
  ShieldPerson20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useConfirmDialog } from '../components/useConfirmDialog';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { useListPageStyles } from '../components/list/useListPageStyles';
import { fmtDate } from '../lib/date';

const usePageStyles = makeStyles({
  rolePill: {
    display: 'inline-block',
    padding: `2px ${tokens.spacingHorizontalS}`,
    borderRadius: tokens.borderRadiusCircular,
    fontSize: tokens.fontSizeBase200,
    fontWeight: tokens.fontWeightMedium,
    textTransform: 'capitalize',
  },
  roleOwner: {
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground1,
  },
  roleMember: {
    backgroundColor: tokens.colorPaletteGreenBackground1,
    color: tokens.colorPaletteGreenForeground2,
  },
  roleViewer: {
    backgroundColor: tokens.colorNeutralBackground3,
    color: tokens.colorNeutralForeground2,
  },
  dialogForm: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  /** Two-line principal cell: alias on top, sub underneath in muted mono. */
  principalCell: {
    display: 'flex',
    flexDirection: 'column',
    gap: '2px',
  },
  principalAlias: {
    fontWeight: tokens.fontWeightSemibold,
    fontSize: tokens.fontSizeBase300,
  },
  principalSub: {
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    fontFamily: tokens.fontFamilyMonospace,
  },
});

interface RoleAssignment {
  id: string;
  principal_type: string;
  principal_id: string;
  scope_id: string;
  role: string;
  granted_at: string;
  granted_by: string;
  /** Admin-set mnemonic; shown instead of principal_id when present. */
  display_alias?: string;
}

function RolePill({
  role,
  classes,
}: {
  role: string;
  classes: ReturnType<typeof usePageStyles>;
}) {
  const cls =
    role === 'owner'
      ? classes.roleOwner
      : role === 'member'
      ? classes.roleMember
      : classes.roleViewer;
  return <span className={`${classes.rolePill} ${cls}`}>{role}</span>;
}

export default function MembersPage() {
  const styles = useListPageStyles();
  const pageStyles = usePageStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const confirmDialog = useConfirmDialog();

  const [inviteOpen, setInviteOpen] = useState(false);
  const [userSub, setUserSub] = useState('');
  const [displayAlias, setDisplayAlias] = useState('');
  const [role, setRole] = useState<'owner' | 'member' | 'viewer'>('member');

  // Find this user's role in the current tenant so we can gate owner-only actions.
  const tenantsQuery = useQuery({
    queryKey: ['tenants'],
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants');
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return data ?? [];
    },
  });
  const myRoles = tenantsQuery.data?.find((t) => t.id === tenantId)?.roles ?? [];
  const isOwner = myRoles.includes('owner');

  const membersQuery = useQuery({
    queryKey: ['members', tenantId],
    enabled: Boolean(tenantId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/members', {
        params: { path: { tenant_id: tenantId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return ((data as { members?: RoleAssignment[] }).members ?? []) as RoleAssignment[];
    },
  });

  const inviteMutation = useMutation({
    mutationFn: async () => {
      const body: { user_sub: string; role: 'owner' | 'member' | 'viewer'; display_alias?: string } = {
        user_sub: userSub.trim(),
        role,
      };
      if (displayAlias.trim()) body.display_alias = displayAlias.trim();

      const { error } = await api.POST('/v1/tenants/{tenant_id}/members', {
        params: { path: { tenant_id: tenantId! } },
        body: body as never,
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Member invited</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['members', tenantId] });
      setInviteOpen(false);
      setUserSub('');
      setDisplayAlias('');
      setRole('member');
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Invite failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const removeMutation = useMutation({
    mutationFn: async (principalId: string) => {
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/members/{principal_id}', {
        params: { path: { tenant_id: tenantId!, principal_id: principalId } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Member removed</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['members', tenantId] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Remove failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const onRemove = async (m: RoleAssignment) => {
    const displayName = m.display_alias ?? m.principal_id;
    const ok = await confirmDialog({
      title: `Remove "${displayName}"?`,
      body: `This will revoke ${m.role} access to the tenant immediately. The user will lose the ability to manage any resources here. This action can be reversed by adding them again.`,
      confirmLabel: 'Remove member',
      destructive: true,
    });
    if (!ok) return;
    removeMutation.mutate(m.principal_id);
  };

  const members = membersQuery.data ?? [];
  const count = members.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <div className={styles.header}>
        <Title2>Access control</Title2>
        <Subtitle1 className={styles.subtitle}>
          Members of tenant <strong>{tenantId}</strong> and their roles.
          {!isOwner && tenantsQuery.isSuccess && ' Read-only — owner role required to invite or remove.'}
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <Button
          appearance="primary"
          icon={<Add20Regular />}
          onClick={() => setInviteOpen(true)}
          disabled={!isOwner}
        >
          Invite member
        </Button>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => membersQuery.refetch()}
          disabled={membersQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {membersQuery.isLoading && <LoadingState label="Loading members…" />}

      {membersQuery.isError && !membersQuery.isLoading && (
        <ErrorState
          message={`Failed to load members: ${(membersQuery.error as Error).message}`}
        />
      )}

      {!membersQuery.isLoading && !membersQuery.isError && count === 0 && (
        <EmptyState
          icon={<ShieldPerson20Regular />}
          title={`No members in ${tenantId ?? 'this tenant'} yet`}
          description="Add members by their OIDC subject and assign them a role. Members can create and manage resources; owners can also invite and remove others."
          action={
            isOwner ? (
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                onClick={() => setInviteOpen(true)}
              >
                Invite member
              </Button>
            ) : undefined
          }
        />
      )}

      {!membersQuery.isLoading && !membersQuery.isError && count === 1 && isOwner && (
        <Subtitle1 style={{ color: tokens.colorNeutralForeground3, fontWeight: 400, fontSize: tokens.fontSizeBase200 }}>
          You&apos;re the only member of this tenant. Use the button above to invite teammates.
        </Subtitle1>
      )}

      {!membersQuery.isLoading && !membersQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Members">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>User</TableHeaderCell>
                <TableHeaderCell>Role</TableHeaderCell>
                <TableHeaderCell>Granted</TableHeaderCell>
                <TableHeaderCell>Granted by</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {members.map((m) => (
                <TableRow key={m.id}>
                  <TableCell>
                    {m.display_alias ? (
                      <div className={pageStyles.principalCell}>
                        <span className={pageStyles.principalAlias}>{m.display_alias}</span>
                        <span className={pageStyles.principalSub}>{m.principal_id}</span>
                      </div>
                    ) : (
                      <span className={styles.tableMonoCell}>{m.principal_id}</span>
                    )}
                  </TableCell>
                  <TableCell><RolePill role={m.role} classes={pageStyles} /></TableCell>
                  <TableCell className={styles.tableMutedCell}>{fmtDate(m.granted_at)}</TableCell>
                  <TableCell className={styles.tableMonoCell} style={{ color: tokens.colorNeutralForeground3 }}>
                    {m.granted_by}
                  </TableCell>
                  <TableCell>
                    {isOwner && (
                      <Menu>
                        <MenuTrigger disableButtonEnhancement>
                          <Button appearance="subtle" icon={<MoreHorizontal20Regular />} aria-label="Actions" />
                        </MenuTrigger>
                        <MenuPopover>
                          <MenuList>
                            <MenuItem onClick={() => onRemove(m)} disabled={removeMutation.isPending}>
                              Remove
                            </MenuItem>
                          </MenuList>
                        </MenuPopover>
                      </Menu>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      <Dialog open={inviteOpen} onOpenChange={(_, d) => setInviteOpen(d.open)}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Invite member</DialogTitle>
            <DialogContent>
              <div className={pageStyles.dialogForm}>
                <Field label="User subject (OIDC sub)" required hint="The user's OIDC `sub` claim — find it on their JWT.">
                  <Input
                    value={userSub}
                    onChange={(_, d) => setUserSub(d.value)}
                    placeholder="01abc123-0000-0000-0000-user000000001"
                  />
                </Field>
                <Field
                  label="Display name (optional)"
                  hint="Mnemonic shown in the members list. Not visible to the user, not synced to the IdP."
                >
                  <Input
                    value={displayAlias}
                    onChange={(_, d) => setDisplayAlias(d.value)}
                    placeholder="e.g. alice-wso2"
                  />
                </Field>
                <Field label="Role" required>
                  <Dropdown
                    value={role}
                    selectedOptions={[role]}
                    onOptionSelect={(_, d) =>
                      setRole((d.optionValue as 'owner' | 'member' | 'viewer') ?? 'member')
                    }
                  >
                    <Option value="owner" text="Owner">
                      Owner — full control, can invite/remove members
                    </Option>
                    <Option value="member" text="Member">
                      Member — can create and manage resources
                    </Option>
                    <Option value="viewer" text="Viewer">
                      Viewer — read-only access
                    </Option>
                  </Dropdown>
                </Field>
              </div>
            </DialogContent>
            <DialogActions>
              <DialogTrigger disableButtonEnhancement>
                <Button appearance="subtle" disabled={inviteMutation.isPending}>
                  Cancel
                </Button>
              </DialogTrigger>
              <Button
                appearance="primary"
                onClick={() => inviteMutation.mutate()}
                disabled={!userSub.trim() || inviteMutation.isPending}
              >
                {inviteMutation.isPending ? 'Inviting…' : 'Invite'}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </div>
  );
}
