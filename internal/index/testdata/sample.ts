import { Injectable } from "@core/di";
import type { User } from "../types/user";
import {
	createLogger,
	type Logger,
} from "./logging/index";
import "./polyfill";
import * as utils from "@shared/utils";

export * from "./re-exports";
export { helper } from "./helpers.ts";

const dynamic = import("./lazy-module");
const legacy = require("./cjs-dep");

// function fakeComment() should not appear
/* class FakeBlock {} */

export interface UserService {
	findAll(query: ListQuery): Promise<User[]>;
	readonly name: string;
}

export type UserID = string;

export type Result =
	| { ok: true }
	| { ok: false };

export enum Role {
	Admin = "admin",
	User = "user",
}

export const MAX_RETRIES = 3;

let mutableCounter = 0;

export const buildQuery = (id: UserID): string => {
	return `select * from users where id = ${id} and role = ${Role.User}`;
};

export async function fetchUser(id: UserID): Promise<User> {
	const pattern = /users\/\d+/g;
	const localHelper = () => id;
	function innerClosure() {
		return localHelper();
	}
	return fetch(`/api/users/${id}`).then((r) => r.json());
}

export default class BaseService implements UserService {
	readonly name = "base";

	constructor(private logger: Logger) {}

	async findAll(query: ListQuery): Promise<User[]> {
		return this.query(query);
	}

	get size(): number {
		return 0;
	}

	private query(q: ListQuery): Promise<User[]> {
		return Promise.resolve([]);
	}
}
