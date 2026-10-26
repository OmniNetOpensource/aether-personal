import { z } from 'zod';

export interface AetherHost {
  session(callback: (value: unknown) => void): void;
  saveSession(token: string, email: string, callback: (saved: boolean) => void): void;
  clearSession(): void;
  uuid(callback: (id: string) => void): void;
  openLink(url: string): void;
  resizeComposer(): void;
  openDesktop(token: string): void;
}

declare const NativeModules: { AetherHost: AetherHost };

const sessionSchema = z.object({
  baseURL: z.url(),
  token: z.string().nullable(),
  email: z.string(),
});

export const host: AetherHost = {
  session: (callback) => NativeModules.AetherHost.session(callback),
  saveSession: (token, email, callback) =>
    NativeModules.AetherHost.saveSession(token, email, callback),
  clearSession: () => NativeModules.AetherHost.clearSession(),
  uuid: (callback) => NativeModules.AetherHost.uuid(callback),
  openLink: (url) => NativeModules.AetherHost.openLink(url),
  resizeComposer: () => NativeModules.AetherHost.resizeComposer(),
  openDesktop: (token) => NativeModules.AetherHost.openDesktop(token),
};
export const readSession = () =>
  new Promise<z.infer<typeof sessionSchema>>((resolve, reject) => {
    host.session((value) => {
      const result = sessionSchema.safeParse(value);
      if (result.success) resolve(result.data);
      else reject(new Error('无法读取登录信息，请重新登录'));
    });
  });

export const saveSession = (token: string, email: string) =>
  new Promise<void>((resolve, reject) => {
    host.saveSession(token, email, (saved) =>
      saved ? resolve() : reject(new Error('无法保存登录信息，请检查设备存储')),
    );
  });
export const messageID = () => new Promise<string>((resolve) => host.uuid(resolve));
