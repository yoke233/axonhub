import { useState, useCallback, useRef, useEffect } from 'react';
import { toast } from 'sonner';
import { useTranslation } from 'react-i18next';
import { copilotOAuthStart, copilotOAuthPoll } from '../data/copilot';
import type { ProxyConfig } from './use-oauth-flow';

export interface DeviceFlowStartResult {
  session_id: string;
  user_code: string;
  verification_uri: string;
  expires_in: number;
  interval: number;
}

export interface DeviceFlowPollResult {
  access_token?: string;
  credentials?: string;
  token_type?: string;
  scope?: string;
  status?: string;
  message?: string;
}

export interface DeviceFlowPollInput {
  session_id: string;
  proxy?: ProxyConfig;
}

export interface UseDeviceFlowOptions {
  /**
   * Callback when credentials are successfully obtained.
   */
  onSuccess?: (accessToken: string) => void;
  startFn?: (headers?: Record<string, string>) => Promise<DeviceFlowStartResult>;
  pollFn?: (input: DeviceFlowPollInput, headers?: Record<string, string>) => Promise<DeviceFlowPollResult>;
  pollInput?: (sessionId: string) => DeviceFlowPollInput;
  getCredentials?: (result: DeviceFlowPollResult) => string | undefined;
}

export interface UseDeviceFlowState {
  userCode: string | null;
  verificationUri: string | null;
  sessionId: string | null;
  expiresAt: number | null;
  interval: number;
  isPolling: boolean;
  error: string | null;
  isComplete: boolean;
}

export interface UseDeviceFlowActions {
  start: () => Promise<void>;
  reset: () => void;
}

/**
 * A hook for managing GitHub Copilot OAuth device flow.
 *
 * Device flow is used when the user cannot be redirected to a callback URL.
 * Instead, the user receives a code to enter on GitHub's device activation page.
 *
 * @example
 * ```tsx
 * const deviceFlow = useDeviceFlow({
 *   onSuccess: (token) => form.setValue('credentials.apiKey', token),
 * });
 *
 * // Display user code and verification URI
 * {deviceFlow.userCode && (
 *   <div>
 *     <p>Enter code: <strong>{deviceFlow.userCode}</strong></p>
 *     <a href={deviceFlow.verificationUri} target="_blank">
 *       Go to {deviceFlow.verificationUri}
 *     </a>
 *   </div>
 * )}
 *
 * // Start the flow
 * <Button onClick={deviceFlow.start} disabled={deviceFlow.isPolling}>
 *   {deviceFlow.isPolling ? 'Waiting for authorization...' : 'Start Device Flow'}
 * </Button>
 * ```
 */
export function useDeviceFlow(
  options: UseDeviceFlowOptions = {}
): UseDeviceFlowState & UseDeviceFlowActions {
  const {
    onSuccess,
    startFn = copilotOAuthStart,
    pollFn = copilotOAuthPoll,
    pollInput = (sessionId: string) => ({ session_id: sessionId }),
    getCredentials = (result: DeviceFlowPollResult) => result.credentials || result.access_token,
  } = options;
  const { t } = useTranslation();

  const [userCode, setUserCode] = useState<string | null>(null);
  const [verificationUri, setVerificationUri] = useState<string | null>(null);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [expiresAt, setExpiresAt] = useState<number | null>(null);
  const [interval, setInterval] = useState(5);
  const [isPolling, setIsPolling] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isComplete, setIsComplete] = useState(false);

  const pollingTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const currentIntervalRef = useRef<number>(5);
  const onSuccessRef = useRef(onSuccess);
  const startFnRef = useRef(startFn);
  const pollFnRef = useRef(pollFn);
  const pollInputRef = useRef(pollInput);
  const getCredentialsRef = useRef(getCredentials);

  useEffect(() => {
    return () => {
      if (pollingTimeoutRef.current) {
        clearTimeout(pollingTimeoutRef.current);
      }
    };
  }, []);

  useEffect(() => {
    onSuccessRef.current = onSuccess;
  }, [onSuccess]);

  useEffect(() => {
    startFnRef.current = startFn;
    pollFnRef.current = pollFn;
    pollInputRef.current = pollInput;
    getCredentialsRef.current = getCredentials;
  }, [startFn, pollFn, pollInput, getCredentials]);

  const poll = useCallback(
    async (sessionId: string, expiry: number) => {
      if (Date.now() >= expiry) {
        setIsPolling(false);
        setError(t('channels.dialogs.oauth.errors.deviceFlowExpired'));
        return;
      }

      try {
        const result: DeviceFlowPollResult = await pollFnRef.current(pollInputRef.current(sessionId));
        const credentials = getCredentialsRef.current(result);

        if (credentials) {
          setIsPolling(false);
          setIsComplete(true);

          if (onSuccessRef.current) {
            onSuccessRef.current(credentials);
          }

          toast.success(t('channels.dialogs.oauth.messages.credentialsImported'));
        } else if (result.status) {
          if (result.status === 'pending') {
            pollingTimeoutRef.current = window.setTimeout(() => {
              poll(sessionId, expiry);
            }, currentIntervalRef.current * 1000);
          } else if (result.status === 'slow_down') {
            const newInterval = currentIntervalRef.current * 2;
            currentIntervalRef.current = newInterval;
            setInterval(newInterval);

            pollingTimeoutRef.current = window.setTimeout(() => {
              poll(sessionId, expiry);
            }, newInterval * 1000);
          } else {
            setIsPolling(false);
            setError(result.message || result.status);
          }
        }
      } catch (err) {
        const errorMessage = err instanceof Error ? err.message : String(err);
        setIsPolling(false);
        setError(errorMessage);
      }
    },
    [t]
  );

  const start = useCallback(async () => {
    if (pollingTimeoutRef.current) {
      clearTimeout(pollingTimeoutRef.current);
      pollingTimeoutRef.current = null;
    }

    setIsPolling(true);
    setError(null);

    try {
      const result: DeviceFlowStartResult = await startFnRef.current();

      setUserCode(result.user_code);
      setVerificationUri(result.verification_uri);
      setSessionId(result.session_id);
      setExpiresAt(Date.now() + result.expires_in * 1000);
      setInterval(result.interval);
      currentIntervalRef.current = result.interval;

      poll(result.session_id, Date.now() + result.expires_in * 1000);
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : String(err);
      setError(errorMessage);
      setIsPolling(false);
    }
  }, [poll]);

  const reset = useCallback(() => {
    if (pollingTimeoutRef.current) {
      clearTimeout(pollingTimeoutRef.current);
      pollingTimeoutRef.current = null;
    }
    setUserCode(null);
    setVerificationUri(null);
    setSessionId(null);
    setExpiresAt(null);
    setInterval(5);
    currentIntervalRef.current = 5;
    setIsPolling(false);
    setError(null);
    setIsComplete(false);
  }, []);

  return {
    userCode,
    verificationUri,
    sessionId,
    expiresAt,
    interval,
    isPolling,
    error,
    isComplete,
    start,
    reset,
  };
}
