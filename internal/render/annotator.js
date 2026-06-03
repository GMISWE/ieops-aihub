/* Vendored bundle of @apache-annotator/dom v0.2.0 (Apache-2.0).
   Exposes window.AnnotatorDom = { describeTextQuote, createTextQuoteSelectorMatcher, highlightText }.
   Regenerate: npm i @apache-annotator/dom@0.2.0 @apache-annotator/selector@0.2.0 esbuild && esbuild entry.js --bundle --format=iife --global-name=AnnotatorDom --outfile=annotator.js
   where entry.js: export { describeTextQuote, createTextQuoteSelectorMatcher, highlightText } from '@apache-annotator/dom';
   Project retired (Apache Attic) — version pinned deliberately. */

var AnnotatorDom = (() => {
  var __create = Object.create;
  var __defProp = Object.defineProperty;
  var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
  var __getOwnPropNames = Object.getOwnPropertyNames;
  var __getProtoOf = Object.getPrototypeOf;
  var __hasOwnProp = Object.prototype.hasOwnProperty;
  var __commonJS = (cb, mod) => function __require() {
    return mod || (0, cb[__getOwnPropNames(cb)[0]])((mod = { exports: {} }).exports, mod), mod.exports;
  };
  var __export = (target, all) => {
    for (var name in all)
      __defProp(target, name, { get: all[name], enumerable: true });
  };
  var __copyProps = (to, from, except, desc) => {
    if (from && typeof from === "object" || typeof from === "function") {
      for (let key of __getOwnPropNames(from))
        if (!__hasOwnProp.call(to, key) && key !== except)
          __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
    }
    return to;
  };
  var __toESM = (mod, isNodeMode, target) => (target = mod != null ? __create(__getProtoOf(mod)) : {}, __copyProps(
    // If the importer is in node compatibility mode or this is not an ESM
    // file that has been converted to a CommonJS file using a Babel-
    // compatible transform (i.e. "__esModule" has not been set), then set
    // "default" to the CommonJS "module.exports" for node compatibility.
    isNodeMode || !mod || !mod.__esModule ? __defProp(target, "default", { value: mod, enumerable: true }) : target,
    mod
  ));
  var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: true }), mod);

  // node_modules/core-js-pure/internals/fails.js
  var require_fails = __commonJS({
    "node_modules/core-js-pure/internals/fails.js"(exports, module) {
      "use strict";
      module.exports = function(exec) {
        try {
          return !!exec();
        } catch (error) {
          return true;
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/function-bind-native.js
  var require_function_bind_native = __commonJS({
    "node_modules/core-js-pure/internals/function-bind-native.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      module.exports = !fails(function() {
        var test = function() {
        }.bind();
        return typeof test != "function" || test.hasOwnProperty("prototype");
      });
    }
  });

  // node_modules/core-js-pure/internals/function-uncurry-this.js
  var require_function_uncurry_this = __commonJS({
    "node_modules/core-js-pure/internals/function-uncurry-this.js"(exports, module) {
      "use strict";
      var NATIVE_BIND = require_function_bind_native();
      var FunctionPrototype = Function.prototype;
      var call = FunctionPrototype.call;
      var uncurryThisWithBind = NATIVE_BIND && FunctionPrototype.bind.bind(call, call);
      module.exports = NATIVE_BIND ? uncurryThisWithBind : function(fn) {
        return function() {
          return call.apply(fn, arguments);
        };
      };
    }
  });

  // node_modules/core-js-pure/internals/object-is-prototype-of.js
  var require_object_is_prototype_of = __commonJS({
    "node_modules/core-js-pure/internals/object-is-prototype-of.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      module.exports = uncurryThis({}.isPrototypeOf);
    }
  });

  // node_modules/core-js-pure/internals/global-this.js
  var require_global_this = __commonJS({
    "node_modules/core-js-pure/internals/global-this.js"(exports, module) {
      "use strict";
      var check = function(it) {
        return it && it.Math === Math && it;
      };
      module.exports = // eslint-disable-next-line es/no-global-this -- safe
      check(typeof globalThis == "object" && globalThis) || check(typeof window == "object" && window) || // eslint-disable-next-line no-restricted-globals -- safe
      check(typeof self == "object" && self) || check(typeof global == "object" && global) || check(typeof exports == "object" && exports) || // eslint-disable-next-line no-new-func -- fallback
      /* @__PURE__ */ (function() {
        return this;
      })() || Function("return this")();
    }
  });

  // node_modules/core-js-pure/internals/function-apply.js
  var require_function_apply = __commonJS({
    "node_modules/core-js-pure/internals/function-apply.js"(exports, module) {
      "use strict";
      var NATIVE_BIND = require_function_bind_native();
      var FunctionPrototype = Function.prototype;
      var apply = FunctionPrototype.apply;
      var call = FunctionPrototype.call;
      module.exports = typeof Reflect == "object" && Reflect.apply || (NATIVE_BIND ? call.bind(apply) : function() {
        return call.apply(apply, arguments);
      });
    }
  });

  // node_modules/core-js-pure/internals/classof-raw.js
  var require_classof_raw = __commonJS({
    "node_modules/core-js-pure/internals/classof-raw.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var toString = uncurryThis({}.toString);
      var stringSlice = uncurryThis("".slice);
      module.exports = function(it) {
        return stringSlice(toString(it), 8, -1);
      };
    }
  });

  // node_modules/core-js-pure/internals/function-uncurry-this-clause.js
  var require_function_uncurry_this_clause = __commonJS({
    "node_modules/core-js-pure/internals/function-uncurry-this-clause.js"(exports, module) {
      "use strict";
      var classofRaw = require_classof_raw();
      var uncurryThis = require_function_uncurry_this();
      module.exports = function(fn) {
        if (classofRaw(fn) === "Function") return uncurryThis(fn);
      };
    }
  });

  // node_modules/core-js-pure/internals/is-callable.js
  var require_is_callable = __commonJS({
    "node_modules/core-js-pure/internals/is-callable.js"(exports, module) {
      "use strict";
      var documentAll = typeof document == "object" && document.all;
      module.exports = typeof documentAll == "undefined" && documentAll !== void 0 ? function(argument) {
        return typeof argument == "function" || argument === documentAll;
      } : function(argument) {
        return typeof argument == "function";
      };
    }
  });

  // node_modules/core-js-pure/internals/descriptors.js
  var require_descriptors = __commonJS({
    "node_modules/core-js-pure/internals/descriptors.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      module.exports = !fails(function() {
        return Object.defineProperty({}, 1, { get: function() {
          return 7;
        } })[1] !== 7;
      });
    }
  });

  // node_modules/core-js-pure/internals/function-call.js
  var require_function_call = __commonJS({
    "node_modules/core-js-pure/internals/function-call.js"(exports, module) {
      "use strict";
      var NATIVE_BIND = require_function_bind_native();
      var call = Function.prototype.call;
      module.exports = NATIVE_BIND ? call.bind(call) : function() {
        return call.apply(call, arguments);
      };
    }
  });

  // node_modules/core-js-pure/internals/object-property-is-enumerable.js
  var require_object_property_is_enumerable = __commonJS({
    "node_modules/core-js-pure/internals/object-property-is-enumerable.js"(exports) {
      "use strict";
      var $propertyIsEnumerable = {}.propertyIsEnumerable;
      var getOwnPropertyDescriptor = Object.getOwnPropertyDescriptor;
      var NASHORN_BUG = getOwnPropertyDescriptor && !$propertyIsEnumerable.call({ 1: 2 }, 1);
      exports.f = NASHORN_BUG ? function propertyIsEnumerable(V) {
        var descriptor = getOwnPropertyDescriptor(this, V);
        return !!descriptor && descriptor.enumerable;
      } : $propertyIsEnumerable;
    }
  });

  // node_modules/core-js-pure/internals/create-property-descriptor.js
  var require_create_property_descriptor = __commonJS({
    "node_modules/core-js-pure/internals/create-property-descriptor.js"(exports, module) {
      "use strict";
      module.exports = function(bitmap, value) {
        return {
          enumerable: !(bitmap & 1),
          configurable: !(bitmap & 2),
          writable: !(bitmap & 4),
          value
        };
      };
    }
  });

  // node_modules/core-js-pure/internals/indexed-object.js
  var require_indexed_object = __commonJS({
    "node_modules/core-js-pure/internals/indexed-object.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var fails = require_fails();
      var classof = require_classof_raw();
      var $Object = Object;
      var split = uncurryThis("".split);
      module.exports = fails(function() {
        return !$Object("z").propertyIsEnumerable(0);
      }) ? function(it) {
        return classof(it) === "String" ? split(it, "") : $Object(it);
      } : $Object;
    }
  });

  // node_modules/core-js-pure/internals/is-null-or-undefined.js
  var require_is_null_or_undefined = __commonJS({
    "node_modules/core-js-pure/internals/is-null-or-undefined.js"(exports, module) {
      "use strict";
      module.exports = function(it) {
        return it === null || it === void 0;
      };
    }
  });

  // node_modules/core-js-pure/internals/require-object-coercible.js
  var require_require_object_coercible = __commonJS({
    "node_modules/core-js-pure/internals/require-object-coercible.js"(exports, module) {
      "use strict";
      var isNullOrUndefined = require_is_null_or_undefined();
      var $TypeError = TypeError;
      module.exports = function(it) {
        if (isNullOrUndefined(it)) throw new $TypeError("Can't call method on " + it);
        return it;
      };
    }
  });

  // node_modules/core-js-pure/internals/to-indexed-object.js
  var require_to_indexed_object = __commonJS({
    "node_modules/core-js-pure/internals/to-indexed-object.js"(exports, module) {
      "use strict";
      var IndexedObject = require_indexed_object();
      var requireObjectCoercible = require_require_object_coercible();
      module.exports = function(it) {
        return IndexedObject(requireObjectCoercible(it));
      };
    }
  });

  // node_modules/core-js-pure/internals/is-object.js
  var require_is_object = __commonJS({
    "node_modules/core-js-pure/internals/is-object.js"(exports, module) {
      "use strict";
      var isCallable = require_is_callable();
      module.exports = function(it) {
        return typeof it == "object" ? it !== null : isCallable(it);
      };
    }
  });

  // node_modules/core-js-pure/internals/path.js
  var require_path = __commonJS({
    "node_modules/core-js-pure/internals/path.js"(exports, module) {
      "use strict";
      module.exports = {};
    }
  });

  // node_modules/core-js-pure/internals/get-built-in.js
  var require_get_built_in = __commonJS({
    "node_modules/core-js-pure/internals/get-built-in.js"(exports, module) {
      "use strict";
      var path = require_path();
      var globalThis2 = require_global_this();
      var isCallable = require_is_callable();
      var aFunction = function(variable) {
        return isCallable(variable) ? variable : void 0;
      };
      module.exports = function(namespace, method) {
        return arguments.length < 2 ? aFunction(path[namespace]) || aFunction(globalThis2[namespace]) : path[namespace] && path[namespace][method] || globalThis2[namespace] && globalThis2[namespace][method];
      };
    }
  });

  // node_modules/core-js-pure/internals/environment-user-agent.js
  var require_environment_user_agent = __commonJS({
    "node_modules/core-js-pure/internals/environment-user-agent.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var navigator = globalThis2.navigator;
      var userAgent = navigator && navigator.userAgent;
      module.exports = userAgent ? String(userAgent) : "";
    }
  });

  // node_modules/core-js-pure/internals/environment-v8-version.js
  var require_environment_v8_version = __commonJS({
    "node_modules/core-js-pure/internals/environment-v8-version.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var userAgent = require_environment_user_agent();
      var process = globalThis2.process;
      var Deno2 = globalThis2.Deno;
      var versions = process && process.versions || Deno2 && Deno2.version;
      var v8 = versions && versions.v8;
      var match;
      var version;
      if (v8) {
        match = v8.split(".");
        version = match[0] > 0 && match[0] < 4 ? 1 : +(match[0] + match[1]);
      }
      if (!version && userAgent) {
        match = userAgent.match(/Edge\/(\d+)/);
        if (!match || match[1] >= 74) {
          match = userAgent.match(/Chrome\/(\d+)/);
          if (match) version = +match[1];
        }
      }
      module.exports = version;
    }
  });

  // node_modules/core-js-pure/internals/symbol-constructor-detection.js
  var require_symbol_constructor_detection = __commonJS({
    "node_modules/core-js-pure/internals/symbol-constructor-detection.js"(exports, module) {
      "use strict";
      var V8_VERSION = require_environment_v8_version();
      var fails = require_fails();
      var globalThis2 = require_global_this();
      var $String = globalThis2.String;
      module.exports = !!Object.getOwnPropertySymbols && !fails(function() {
        var symbol = /* @__PURE__ */ Symbol("symbol detection");
        return !$String(symbol) || !(Object(symbol) instanceof Symbol) || // Chrome 38-40 symbols are not inherited from DOM collections prototypes to instances
        !Symbol.sham && V8_VERSION && V8_VERSION < 41;
      });
    }
  });

  // node_modules/core-js-pure/internals/use-symbol-as-uid.js
  var require_use_symbol_as_uid = __commonJS({
    "node_modules/core-js-pure/internals/use-symbol-as-uid.js"(exports, module) {
      "use strict";
      var NATIVE_SYMBOL = require_symbol_constructor_detection();
      module.exports = NATIVE_SYMBOL && !Symbol.sham && typeof Symbol.iterator == "symbol";
    }
  });

  // node_modules/core-js-pure/internals/is-symbol.js
  var require_is_symbol = __commonJS({
    "node_modules/core-js-pure/internals/is-symbol.js"(exports, module) {
      "use strict";
      var getBuiltIn = require_get_built_in();
      var isCallable = require_is_callable();
      var isPrototypeOf = require_object_is_prototype_of();
      var USE_SYMBOL_AS_UID = require_use_symbol_as_uid();
      var $Object = Object;
      module.exports = USE_SYMBOL_AS_UID ? function(it) {
        return typeof it == "symbol";
      } : function(it) {
        var $Symbol = getBuiltIn("Symbol");
        return isCallable($Symbol) && isPrototypeOf($Symbol.prototype, $Object(it));
      };
    }
  });

  // node_modules/core-js-pure/internals/try-to-string.js
  var require_try_to_string = __commonJS({
    "node_modules/core-js-pure/internals/try-to-string.js"(exports, module) {
      "use strict";
      var $String = String;
      module.exports = function(argument) {
        try {
          return $String(argument);
        } catch (error) {
          return "Object";
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/a-callable.js
  var require_a_callable = __commonJS({
    "node_modules/core-js-pure/internals/a-callable.js"(exports, module) {
      "use strict";
      var isCallable = require_is_callable();
      var tryToString = require_try_to_string();
      var $TypeError = TypeError;
      module.exports = function(argument) {
        if (isCallable(argument)) return argument;
        throw new $TypeError(tryToString(argument) + " is not a function");
      };
    }
  });

  // node_modules/core-js-pure/internals/get-method.js
  var require_get_method = __commonJS({
    "node_modules/core-js-pure/internals/get-method.js"(exports, module) {
      "use strict";
      var aCallable = require_a_callable();
      var isNullOrUndefined = require_is_null_or_undefined();
      module.exports = function(V, P) {
        var func = V[P];
        return isNullOrUndefined(func) ? void 0 : aCallable(func);
      };
    }
  });

  // node_modules/core-js-pure/internals/ordinary-to-primitive.js
  var require_ordinary_to_primitive = __commonJS({
    "node_modules/core-js-pure/internals/ordinary-to-primitive.js"(exports, module) {
      "use strict";
      var call = require_function_call();
      var isCallable = require_is_callable();
      var isObject = require_is_object();
      var $TypeError = TypeError;
      module.exports = function(input, pref) {
        var fn, val;
        if (pref === "string" && isCallable(fn = input.toString) && !isObject(val = call(fn, input))) return val;
        if (isCallable(fn = input.valueOf) && !isObject(val = call(fn, input))) return val;
        if (pref !== "string" && isCallable(fn = input.toString) && !isObject(val = call(fn, input))) return val;
        throw new $TypeError("Can't convert object to primitive value");
      };
    }
  });

  // node_modules/core-js-pure/internals/is-pure.js
  var require_is_pure = __commonJS({
    "node_modules/core-js-pure/internals/is-pure.js"(exports, module) {
      "use strict";
      module.exports = true;
    }
  });

  // node_modules/core-js-pure/internals/define-global-property.js
  var require_define_global_property = __commonJS({
    "node_modules/core-js-pure/internals/define-global-property.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var defineProperty = Object.defineProperty;
      module.exports = function(key, value) {
        try {
          defineProperty(globalThis2, key, { value, configurable: true, writable: true });
        } catch (error) {
          globalThis2[key] = value;
        }
        return value;
      };
    }
  });

  // node_modules/core-js-pure/internals/shared-store.js
  var require_shared_store = __commonJS({
    "node_modules/core-js-pure/internals/shared-store.js"(exports, module) {
      "use strict";
      var IS_PURE = require_is_pure();
      var globalThis2 = require_global_this();
      var defineGlobalProperty = require_define_global_property();
      var SHARED = "__core-js_shared__";
      var store = module.exports = globalThis2[SHARED] || defineGlobalProperty(SHARED, {});
      (store.versions || (store.versions = [])).push({
        version: "3.49.0",
        mode: IS_PURE ? "pure" : "global",
        copyright: "\xA9 2013\u20132025 Denis Pushkarev (zloirock.ru), 2025\u20132026 CoreJS Company (core-js.io). All rights reserved.",
        license: "https://github.com/zloirock/core-js/blob/v3.49.0/LICENSE",
        source: "https://github.com/zloirock/core-js"
      });
    }
  });

  // node_modules/core-js-pure/internals/shared.js
  var require_shared = __commonJS({
    "node_modules/core-js-pure/internals/shared.js"(exports, module) {
      "use strict";
      var store = require_shared_store();
      module.exports = function(key, value) {
        return store[key] || (store[key] = value || {});
      };
    }
  });

  // node_modules/core-js-pure/internals/to-object.js
  var require_to_object = __commonJS({
    "node_modules/core-js-pure/internals/to-object.js"(exports, module) {
      "use strict";
      var requireObjectCoercible = require_require_object_coercible();
      var $Object = Object;
      module.exports = function(argument) {
        return $Object(requireObjectCoercible(argument));
      };
    }
  });

  // node_modules/core-js-pure/internals/has-own-property.js
  var require_has_own_property = __commonJS({
    "node_modules/core-js-pure/internals/has-own-property.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var toObject = require_to_object();
      var hasOwnProperty = uncurryThis({}.hasOwnProperty);
      module.exports = Object.hasOwn || function hasOwn(it, key) {
        return hasOwnProperty(toObject(it), key);
      };
    }
  });

  // node_modules/core-js-pure/internals/uid.js
  var require_uid = __commonJS({
    "node_modules/core-js-pure/internals/uid.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var id = 0;
      var postfix = Math.random();
      var toString = uncurryThis(1.1.toString);
      module.exports = function(key) {
        return "Symbol(" + (key === void 0 ? "" : key) + ")_" + toString(++id + postfix, 36);
      };
    }
  });

  // node_modules/core-js-pure/internals/well-known-symbol.js
  var require_well_known_symbol = __commonJS({
    "node_modules/core-js-pure/internals/well-known-symbol.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var shared = require_shared();
      var hasOwn = require_has_own_property();
      var uid = require_uid();
      var NATIVE_SYMBOL = require_symbol_constructor_detection();
      var USE_SYMBOL_AS_UID = require_use_symbol_as_uid();
      var Symbol2 = globalThis2.Symbol;
      var WellKnownSymbolsStore = shared("wks");
      var createWellKnownSymbol = USE_SYMBOL_AS_UID ? Symbol2["for"] || Symbol2 : Symbol2 && Symbol2.withoutSetter || uid;
      module.exports = function(name) {
        if (!hasOwn(WellKnownSymbolsStore, name)) {
          WellKnownSymbolsStore[name] = NATIVE_SYMBOL && hasOwn(Symbol2, name) ? Symbol2[name] : createWellKnownSymbol("Symbol." + name);
        }
        return WellKnownSymbolsStore[name];
      };
    }
  });

  // node_modules/core-js-pure/internals/to-primitive.js
  var require_to_primitive = __commonJS({
    "node_modules/core-js-pure/internals/to-primitive.js"(exports, module) {
      "use strict";
      var call = require_function_call();
      var isObject = require_is_object();
      var isSymbol = require_is_symbol();
      var getMethod = require_get_method();
      var ordinaryToPrimitive = require_ordinary_to_primitive();
      var wellKnownSymbol = require_well_known_symbol();
      var $TypeError = TypeError;
      var TO_PRIMITIVE = wellKnownSymbol("toPrimitive");
      module.exports = function(input, pref) {
        if (!isObject(input) || isSymbol(input)) return input;
        var exoticToPrim = getMethod(input, TO_PRIMITIVE);
        var result;
        if (exoticToPrim) {
          if (pref === void 0) pref = "default";
          result = call(exoticToPrim, input, pref);
          if (!isObject(result) || isSymbol(result)) return result;
          throw new $TypeError("Can't convert object to primitive value");
        }
        if (pref === void 0) pref = "number";
        return ordinaryToPrimitive(input, pref);
      };
    }
  });

  // node_modules/core-js-pure/internals/to-property-key.js
  var require_to_property_key = __commonJS({
    "node_modules/core-js-pure/internals/to-property-key.js"(exports, module) {
      "use strict";
      var toPrimitive2 = require_to_primitive();
      var isSymbol = require_is_symbol();
      module.exports = function(argument) {
        var key = toPrimitive2(argument, "string");
        return isSymbol(key) ? key : key + "";
      };
    }
  });

  // node_modules/core-js-pure/internals/document-create-element.js
  var require_document_create_element = __commonJS({
    "node_modules/core-js-pure/internals/document-create-element.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var isObject = require_is_object();
      var document2 = globalThis2.document;
      var EXISTS = isObject(document2) && isObject(document2.createElement);
      module.exports = function(it) {
        return EXISTS ? document2.createElement(it) : {};
      };
    }
  });

  // node_modules/core-js-pure/internals/ie8-dom-define.js
  var require_ie8_dom_define = __commonJS({
    "node_modules/core-js-pure/internals/ie8-dom-define.js"(exports, module) {
      "use strict";
      var DESCRIPTORS = require_descriptors();
      var fails = require_fails();
      var createElement = require_document_create_element();
      module.exports = !DESCRIPTORS && !fails(function() {
        return Object.defineProperty(createElement("div"), "a", {
          get: function() {
            return 7;
          }
        }).a !== 7;
      });
    }
  });

  // node_modules/core-js-pure/internals/object-get-own-property-descriptor.js
  var require_object_get_own_property_descriptor = __commonJS({
    "node_modules/core-js-pure/internals/object-get-own-property-descriptor.js"(exports) {
      "use strict";
      var DESCRIPTORS = require_descriptors();
      var call = require_function_call();
      var propertyIsEnumerableModule = require_object_property_is_enumerable();
      var createPropertyDescriptor = require_create_property_descriptor();
      var toIndexedObject = require_to_indexed_object();
      var toPropertyKey2 = require_to_property_key();
      var hasOwn = require_has_own_property();
      var IE8_DOM_DEFINE = require_ie8_dom_define();
      var $getOwnPropertyDescriptor = Object.getOwnPropertyDescriptor;
      exports.f = DESCRIPTORS ? $getOwnPropertyDescriptor : function getOwnPropertyDescriptor(O, P) {
        O = toIndexedObject(O);
        P = toPropertyKey2(P);
        if (IE8_DOM_DEFINE) try {
          return $getOwnPropertyDescriptor(O, P);
        } catch (error) {
        }
        if (hasOwn(O, P)) return createPropertyDescriptor(!call(propertyIsEnumerableModule.f, O, P), O[P]);
      };
    }
  });

  // node_modules/core-js-pure/internals/is-forced.js
  var require_is_forced = __commonJS({
    "node_modules/core-js-pure/internals/is-forced.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      var isCallable = require_is_callable();
      var replacement = /#|\.prototype\./;
      var isForced = function(feature, detection) {
        var value = data[normalize(feature)];
        return value === POLYFILL ? true : value === NATIVE ? false : isCallable(detection) ? fails(detection) : !!detection;
      };
      var normalize = isForced.normalize = function(string) {
        return String(string).replace(replacement, ".").toLowerCase();
      };
      var data = isForced.data = {};
      var NATIVE = isForced.NATIVE = "N";
      var POLYFILL = isForced.POLYFILL = "P";
      module.exports = isForced;
    }
  });

  // node_modules/core-js-pure/internals/function-bind-context.js
  var require_function_bind_context = __commonJS({
    "node_modules/core-js-pure/internals/function-bind-context.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this_clause();
      var aCallable = require_a_callable();
      var NATIVE_BIND = require_function_bind_native();
      var bind = uncurryThis(uncurryThis.bind);
      module.exports = function(fn, that) {
        aCallable(fn);
        return that === void 0 ? fn : NATIVE_BIND ? bind(fn, that) : function() {
          return fn.apply(that, arguments);
        };
      };
    }
  });

  // node_modules/core-js-pure/internals/v8-prototype-define-bug.js
  var require_v8_prototype_define_bug = __commonJS({
    "node_modules/core-js-pure/internals/v8-prototype-define-bug.js"(exports, module) {
      "use strict";
      var DESCRIPTORS = require_descriptors();
      var fails = require_fails();
      module.exports = DESCRIPTORS && fails(function() {
        return Object.defineProperty(function() {
        }, "prototype", {
          value: 42,
          writable: false
        }).prototype !== 42;
      });
    }
  });

  // node_modules/core-js-pure/internals/an-object.js
  var require_an_object = __commonJS({
    "node_modules/core-js-pure/internals/an-object.js"(exports, module) {
      "use strict";
      var isObject = require_is_object();
      var $String = String;
      var $TypeError = TypeError;
      module.exports = function(argument) {
        if (isObject(argument)) return argument;
        throw new $TypeError($String(argument) + " is not an object");
      };
    }
  });

  // node_modules/core-js-pure/internals/object-define-property.js
  var require_object_define_property = __commonJS({
    "node_modules/core-js-pure/internals/object-define-property.js"(exports) {
      "use strict";
      var DESCRIPTORS = require_descriptors();
      var IE8_DOM_DEFINE = require_ie8_dom_define();
      var V8_PROTOTYPE_DEFINE_BUG = require_v8_prototype_define_bug();
      var anObject = require_an_object();
      var toPropertyKey2 = require_to_property_key();
      var $TypeError = TypeError;
      var $defineProperty = Object.defineProperty;
      var $getOwnPropertyDescriptor = Object.getOwnPropertyDescriptor;
      var ENUMERABLE = "enumerable";
      var CONFIGURABLE = "configurable";
      var WRITABLE = "writable";
      exports.f = DESCRIPTORS ? V8_PROTOTYPE_DEFINE_BUG ? function defineProperty(O, P, Attributes) {
        anObject(O);
        P = toPropertyKey2(P);
        anObject(Attributes);
        if (typeof O === "function" && P === "prototype" && "value" in Attributes && WRITABLE in Attributes && !Attributes[WRITABLE]) {
          var current = $getOwnPropertyDescriptor(O, P);
          if (current && current[WRITABLE]) {
            O[P] = Attributes.value;
            Attributes = {
              configurable: CONFIGURABLE in Attributes ? Attributes[CONFIGURABLE] : current[CONFIGURABLE],
              enumerable: ENUMERABLE in Attributes ? Attributes[ENUMERABLE] : current[ENUMERABLE],
              writable: false
            };
          }
        }
        return $defineProperty(O, P, Attributes);
      } : $defineProperty : function defineProperty(O, P, Attributes) {
        anObject(O);
        P = toPropertyKey2(P);
        anObject(Attributes);
        if (IE8_DOM_DEFINE) try {
          return $defineProperty(O, P, Attributes);
        } catch (error) {
        }
        if ("get" in Attributes || "set" in Attributes) throw new $TypeError("Accessors not supported");
        if ("value" in Attributes) O[P] = Attributes.value;
        return O;
      };
    }
  });

  // node_modules/core-js-pure/internals/create-non-enumerable-property.js
  var require_create_non_enumerable_property = __commonJS({
    "node_modules/core-js-pure/internals/create-non-enumerable-property.js"(exports, module) {
      "use strict";
      var DESCRIPTORS = require_descriptors();
      var definePropertyModule = require_object_define_property();
      var createPropertyDescriptor = require_create_property_descriptor();
      module.exports = DESCRIPTORS ? function(object, key, value) {
        return definePropertyModule.f(object, key, createPropertyDescriptor(1, value));
      } : function(object, key, value) {
        object[key] = value;
        return object;
      };
    }
  });

  // node_modules/core-js-pure/internals/export.js
  var require_export = __commonJS({
    "node_modules/core-js-pure/internals/export.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var apply = require_function_apply();
      var uncurryThis = require_function_uncurry_this_clause();
      var isCallable = require_is_callable();
      var getOwnPropertyDescriptor = require_object_get_own_property_descriptor().f;
      var isForced = require_is_forced();
      var path = require_path();
      var bind = require_function_bind_context();
      var createNonEnumerableProperty = require_create_non_enumerable_property();
      var hasOwn = require_has_own_property();
      require_shared_store();
      var wrapConstructor = function(NativeConstructor) {
        var Wrapper = function(a, b, c) {
          if (this instanceof Wrapper) {
            switch (arguments.length) {
              case 0:
                return new NativeConstructor();
              case 1:
                return new NativeConstructor(a);
              case 2:
                return new NativeConstructor(a, b);
            }
            return new NativeConstructor(a, b, c);
          }
          return apply(NativeConstructor, this, arguments);
        };
        Wrapper.prototype = NativeConstructor.prototype;
        return Wrapper;
      };
      module.exports = function(options, source) {
        var TARGET = options.target;
        var GLOBAL = options.global;
        var STATIC = options.stat;
        var PROTO = options.proto;
        var nativeSource = GLOBAL ? globalThis2 : STATIC ? globalThis2[TARGET] : globalThis2[TARGET] && globalThis2[TARGET].prototype;
        var target = GLOBAL ? path : path[TARGET] || createNonEnumerableProperty(path, TARGET, {})[TARGET];
        var targetPrototype = target.prototype;
        var FORCED, USE_NATIVE, VIRTUAL_PROTOTYPE;
        var key, sourceProperty, targetProperty, nativeProperty, resultProperty, descriptor;
        for (key in source) {
          FORCED = isForced(GLOBAL ? key : TARGET + (STATIC ? "." : "#") + key, options.forced);
          USE_NATIVE = !FORCED && nativeSource && hasOwn(nativeSource, key);
          targetProperty = target[key];
          if (USE_NATIVE) if (options.dontCallGetSet) {
            descriptor = getOwnPropertyDescriptor(nativeSource, key);
            nativeProperty = descriptor && descriptor.value;
          } else nativeProperty = nativeSource[key];
          sourceProperty = USE_NATIVE && nativeProperty ? nativeProperty : source[key];
          if (!FORCED && !PROTO && typeof targetProperty == typeof sourceProperty) continue;
          if (options.bind && USE_NATIVE) resultProperty = bind(sourceProperty, globalThis2);
          else if (options.wrap && USE_NATIVE) resultProperty = wrapConstructor(sourceProperty);
          else if (PROTO && isCallable(sourceProperty)) resultProperty = uncurryThis(sourceProperty);
          else resultProperty = sourceProperty;
          if (options.sham || sourceProperty && sourceProperty.sham || targetProperty && targetProperty.sham) {
            createNonEnumerableProperty(resultProperty, "sham", true);
          }
          createNonEnumerableProperty(target, key, resultProperty);
          if (PROTO) {
            VIRTUAL_PROTOTYPE = TARGET + "Prototype";
            if (!hasOwn(path, VIRTUAL_PROTOTYPE)) {
              createNonEnumerableProperty(path, VIRTUAL_PROTOTYPE, {});
            }
            createNonEnumerableProperty(path[VIRTUAL_PROTOTYPE], key, sourceProperty);
            if (options.real && targetPrototype && (FORCED || !targetPrototype[key])) {
              createNonEnumerableProperty(targetPrototype, key, sourceProperty);
            }
          }
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/is-array.js
  var require_is_array = __commonJS({
    "node_modules/core-js-pure/internals/is-array.js"(exports, module) {
      "use strict";
      var classof = require_classof_raw();
      module.exports = Array.isArray || function isArray(argument) {
        return classof(argument) === "Array";
      };
    }
  });

  // node_modules/core-js-pure/internals/to-string-tag-support.js
  var require_to_string_tag_support = __commonJS({
    "node_modules/core-js-pure/internals/to-string-tag-support.js"(exports, module) {
      "use strict";
      var wellKnownSymbol = require_well_known_symbol();
      var TO_STRING_TAG = wellKnownSymbol("toStringTag");
      var test = {};
      test[TO_STRING_TAG] = "z";
      module.exports = String(test) === "[object z]";
    }
  });

  // node_modules/core-js-pure/internals/classof.js
  var require_classof = __commonJS({
    "node_modules/core-js-pure/internals/classof.js"(exports, module) {
      "use strict";
      var TO_STRING_TAG_SUPPORT = require_to_string_tag_support();
      var isCallable = require_is_callable();
      var classofRaw = require_classof_raw();
      var wellKnownSymbol = require_well_known_symbol();
      var TO_STRING_TAG = wellKnownSymbol("toStringTag");
      var $Object = Object;
      var CORRECT_ARGUMENTS = classofRaw(/* @__PURE__ */ (function() {
        return arguments;
      })()) === "Arguments";
      var tryGet = function(it, key) {
        try {
          return it[key];
        } catch (error) {
        }
      };
      module.exports = TO_STRING_TAG_SUPPORT ? classofRaw : function(it) {
        var O, tag, result;
        return it === void 0 ? "Undefined" : it === null ? "Null" : typeof (tag = tryGet(O = $Object(it), TO_STRING_TAG)) == "string" ? tag : CORRECT_ARGUMENTS ? classofRaw(O) : (result = classofRaw(O)) === "Object" && isCallable(O.callee) ? "Arguments" : result;
      };
    }
  });

  // node_modules/core-js-pure/internals/inspect-source.js
  var require_inspect_source = __commonJS({
    "node_modules/core-js-pure/internals/inspect-source.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var isCallable = require_is_callable();
      var store = require_shared_store();
      var functionToString = uncurryThis(Function.toString);
      if (!isCallable(store.inspectSource)) {
        store.inspectSource = function(it) {
          return functionToString(it);
        };
      }
      module.exports = store.inspectSource;
    }
  });

  // node_modules/core-js-pure/internals/is-constructor.js
  var require_is_constructor = __commonJS({
    "node_modules/core-js-pure/internals/is-constructor.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var fails = require_fails();
      var isCallable = require_is_callable();
      var classof = require_classof();
      var getBuiltIn = require_get_built_in();
      var inspectSource = require_inspect_source();
      var noop = function() {
      };
      var construct = getBuiltIn("Reflect", "construct");
      var constructorRegExp = /^\s*(?:class|function)\b/;
      var exec = uncurryThis(constructorRegExp.exec);
      var INCORRECT_TO_STRING = !constructorRegExp.test(noop);
      var isConstructorModern = function isConstructor(argument) {
        if (!isCallable(argument)) return false;
        try {
          construct(noop, [], argument);
          return true;
        } catch (error) {
          return false;
        }
      };
      var isConstructorLegacy = function isConstructor(argument) {
        if (!isCallable(argument)) return false;
        switch (classof(argument)) {
          case "AsyncFunction":
          case "GeneratorFunction":
          case "AsyncGeneratorFunction":
            return false;
        }
        try {
          return INCORRECT_TO_STRING || !!exec(constructorRegExp, inspectSource(argument));
        } catch (error) {
          return true;
        }
      };
      isConstructorLegacy.sham = true;
      module.exports = !construct || fails(function() {
        var called;
        return isConstructorModern(isConstructorModern.call) || !isConstructorModern(Object) || !isConstructorModern(function() {
          called = true;
        }) || called;
      }) ? isConstructorLegacy : isConstructorModern;
    }
  });

  // node_modules/core-js-pure/internals/math-trunc.js
  var require_math_trunc = __commonJS({
    "node_modules/core-js-pure/internals/math-trunc.js"(exports, module) {
      "use strict";
      var ceil = Math.ceil;
      var floor = Math.floor;
      module.exports = Math.trunc || function trunc(x) {
        var n = +x;
        return (n > 0 ? floor : ceil)(n);
      };
    }
  });

  // node_modules/core-js-pure/internals/to-integer-or-infinity.js
  var require_to_integer_or_infinity = __commonJS({
    "node_modules/core-js-pure/internals/to-integer-or-infinity.js"(exports, module) {
      "use strict";
      var trunc = require_math_trunc();
      module.exports = function(argument) {
        var number = +argument;
        return number !== number || number === 0 ? 0 : trunc(number);
      };
    }
  });

  // node_modules/core-js-pure/internals/to-absolute-index.js
  var require_to_absolute_index = __commonJS({
    "node_modules/core-js-pure/internals/to-absolute-index.js"(exports, module) {
      "use strict";
      var toIntegerOrInfinity = require_to_integer_or_infinity();
      var max = Math.max;
      var min = Math.min;
      module.exports = function(index, length) {
        var integer = toIntegerOrInfinity(index);
        return integer < 0 ? max(integer + length, 0) : min(integer, length);
      };
    }
  });

  // node_modules/core-js-pure/internals/to-length.js
  var require_to_length = __commonJS({
    "node_modules/core-js-pure/internals/to-length.js"(exports, module) {
      "use strict";
      var toIntegerOrInfinity = require_to_integer_or_infinity();
      var min = Math.min;
      module.exports = function(argument) {
        var len = toIntegerOrInfinity(argument);
        return len > 0 ? min(len, 9007199254740991) : 0;
      };
    }
  });

  // node_modules/core-js-pure/internals/length-of-array-like.js
  var require_length_of_array_like = __commonJS({
    "node_modules/core-js-pure/internals/length-of-array-like.js"(exports, module) {
      "use strict";
      var toLength = require_to_length();
      module.exports = function(obj) {
        return toLength(obj.length);
      };
    }
  });

  // node_modules/core-js-pure/internals/create-property.js
  var require_create_property = __commonJS({
    "node_modules/core-js-pure/internals/create-property.js"(exports, module) {
      "use strict";
      var DESCRIPTORS = require_descriptors();
      var definePropertyModule = require_object_define_property();
      var createPropertyDescriptor = require_create_property_descriptor();
      module.exports = function(object, key, value) {
        if (DESCRIPTORS) definePropertyModule.f(object, key, createPropertyDescriptor(0, value));
        else object[key] = value;
      };
    }
  });

  // node_modules/core-js-pure/internals/array-set-length.js
  var require_array_set_length = __commonJS({
    "node_modules/core-js-pure/internals/array-set-length.js"(exports, module) {
      "use strict";
      var DESCRIPTORS = require_descriptors();
      var isArray = require_is_array();
      var $TypeError = TypeError;
      var getOwnPropertyDescriptor = Object.getOwnPropertyDescriptor;
      var SILENT_ON_NON_WRITABLE_LENGTH_SET = DESCRIPTORS && !(function() {
        if (this !== void 0) return true;
        try {
          Object.defineProperty([], "length", { writable: false }).length = 1;
        } catch (error) {
          return error instanceof TypeError;
        }
      })();
      module.exports = SILENT_ON_NON_WRITABLE_LENGTH_SET ? function(O, length) {
        if (isArray(O) && !getOwnPropertyDescriptor(O, "length").writable) {
          throw new $TypeError("Cannot set read only .length");
        }
        return O.length = length;
      } : function(O, length) {
        return O.length = length;
      };
    }
  });

  // node_modules/core-js-pure/internals/array-method-has-species-support.js
  var require_array_method_has_species_support = __commonJS({
    "node_modules/core-js-pure/internals/array-method-has-species-support.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      var wellKnownSymbol = require_well_known_symbol();
      var V8_VERSION = require_environment_v8_version();
      var SPECIES = wellKnownSymbol("species");
      module.exports = function(METHOD_NAME) {
        return V8_VERSION >= 51 || !fails(function() {
          var array = [];
          var constructor = array.constructor = {};
          constructor[SPECIES] = function() {
            return { foo: 1 };
          };
          return array[METHOD_NAME](Boolean).foo !== 1;
        });
      };
    }
  });

  // node_modules/core-js-pure/internals/array-slice.js
  var require_array_slice = __commonJS({
    "node_modules/core-js-pure/internals/array-slice.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      module.exports = uncurryThis([].slice);
    }
  });

  // node_modules/core-js-pure/modules/es.array.slice.js
  var require_es_array_slice = __commonJS({
    "node_modules/core-js-pure/modules/es.array.slice.js"() {
      "use strict";
      var $ = require_export();
      var isArray = require_is_array();
      var isConstructor = require_is_constructor();
      var isObject = require_is_object();
      var toAbsoluteIndex = require_to_absolute_index();
      var lengthOfArrayLike = require_length_of_array_like();
      var toIndexedObject = require_to_indexed_object();
      var createProperty = require_create_property();
      var setArrayLength = require_array_set_length();
      var wellKnownSymbol = require_well_known_symbol();
      var arrayMethodHasSpeciesSupport = require_array_method_has_species_support();
      var nativeSlice = require_array_slice();
      var HAS_SPECIES_SUPPORT = arrayMethodHasSpeciesSupport("slice");
      var SPECIES = wellKnownSymbol("species");
      var $Array = Array;
      var max = Math.max;
      $({ target: "Array", proto: true, forced: !HAS_SPECIES_SUPPORT }, {
        slice: function slice(start, end) {
          var O = toIndexedObject(this);
          var length = lengthOfArrayLike(O);
          var k = toAbsoluteIndex(start, length);
          var fin = toAbsoluteIndex(end === void 0 ? length : end, length);
          var Constructor, result, n;
          if (isArray(O)) {
            Constructor = O.constructor;
            if (isConstructor(Constructor) && (Constructor === $Array || isArray(Constructor.prototype))) {
              Constructor = void 0;
            } else if (isObject(Constructor)) {
              Constructor = Constructor[SPECIES];
              if (Constructor === null) Constructor = void 0;
            }
            if (Constructor === $Array || Constructor === void 0) {
              return nativeSlice(O, k, fin);
            }
          }
          result = new (Constructor === void 0 ? $Array : Constructor)(max(fin - k, 0));
          for (n = 0; k < fin; k++, n++) if (k in O) createProperty(result, n, O[k]);
          setArrayLength(result, n);
          return result;
        }
      });
    }
  });

  // node_modules/core-js-pure/internals/get-built-in-prototype-method.js
  var require_get_built_in_prototype_method = __commonJS({
    "node_modules/core-js-pure/internals/get-built-in-prototype-method.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var path = require_path();
      module.exports = function(CONSTRUCTOR, METHOD) {
        var Namespace = path[CONSTRUCTOR + "Prototype"];
        var pureMethod = Namespace && Namespace[METHOD];
        if (pureMethod) return pureMethod;
        var NativeConstructor = globalThis2[CONSTRUCTOR];
        var NativePrototype = NativeConstructor && NativeConstructor.prototype;
        return NativePrototype && NativePrototype[METHOD];
      };
    }
  });

  // node_modules/core-js-pure/es/array/virtual/slice.js
  var require_slice = __commonJS({
    "node_modules/core-js-pure/es/array/virtual/slice.js"(exports, module) {
      "use strict";
      require_es_array_slice();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Array", "slice");
    }
  });

  // node_modules/core-js-pure/es/instance/slice.js
  var require_slice2 = __commonJS({
    "node_modules/core-js-pure/es/instance/slice.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_slice();
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        var own = it.slice;
        return it === ArrayPrototype || isPrototypeOf(ArrayPrototype, it) && own === ArrayPrototype.slice ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/slice.js
  var require_slice3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/slice.js"(exports, module) {
      "use strict";
      var parent = require_slice2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/slice.js
  var require_slice4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/slice.js"(exports, module) {
      "use strict";
      var parent = require_slice3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/slice.js
  var require_slice5 = __commonJS({
    "node_modules/core-js-pure/full/instance/slice.js"(exports, module) {
      "use strict";
      var parent = require_slice4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/slice.js
  var require_slice6 = __commonJS({
    "node_modules/core-js-pure/features/instance/slice.js"(exports, module) {
      "use strict";
      module.exports = require_slice5();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/instance/slice.js
  var require_slice7 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/instance/slice.js"(exports, module) {
      module.exports = require_slice6();
    }
  });

  // node_modules/core-js-pure/internals/to-string.js
  var require_to_string = __commonJS({
    "node_modules/core-js-pure/internals/to-string.js"(exports, module) {
      "use strict";
      var classof = require_classof();
      var $String = String;
      module.exports = function(argument) {
        if (classof(argument) === "Symbol") throw new TypeError("Cannot convert a Symbol value to a string");
        return $String(argument);
      };
    }
  });

  // node_modules/core-js-pure/internals/string-multibyte.js
  var require_string_multibyte = __commonJS({
    "node_modules/core-js-pure/internals/string-multibyte.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var toIntegerOrInfinity = require_to_integer_or_infinity();
      var toString = require_to_string();
      var requireObjectCoercible = require_require_object_coercible();
      var charAt = uncurryThis("".charAt);
      var charCodeAt = uncurryThis("".charCodeAt);
      var stringSlice = uncurryThis("".slice);
      var createMethod = function(CONVERT_TO_STRING) {
        return function($this, pos) {
          var S = toString(requireObjectCoercible($this));
          var position = toIntegerOrInfinity(pos);
          var size = S.length;
          var first, second;
          if (position < 0 || position >= size) return CONVERT_TO_STRING ? "" : void 0;
          first = charCodeAt(S, position);
          return first < 55296 || first > 56319 || position + 1 === size || (second = charCodeAt(S, position + 1)) < 56320 || second > 57343 ? CONVERT_TO_STRING ? charAt(S, position) : first : CONVERT_TO_STRING ? stringSlice(S, position, position + 2) : (first - 55296 << 10) + (second - 56320) + 65536;
        };
      };
      module.exports = {
        // `String.prototype.codePointAt` method
        // https://tc39.es/ecma262/#sec-string.prototype.codepointat
        codeAt: createMethod(false),
        // `String.prototype.at` method
        // https://github.com/mathiasbynens/String.prototype.at
        charAt: createMethod(true)
      };
    }
  });

  // node_modules/core-js-pure/internals/weak-map-basic-detection.js
  var require_weak_map_basic_detection = __commonJS({
    "node_modules/core-js-pure/internals/weak-map-basic-detection.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var isCallable = require_is_callable();
      var WeakMap = globalThis2.WeakMap;
      module.exports = isCallable(WeakMap) && /native code/.test(String(WeakMap));
    }
  });

  // node_modules/core-js-pure/internals/shared-key.js
  var require_shared_key = __commonJS({
    "node_modules/core-js-pure/internals/shared-key.js"(exports, module) {
      "use strict";
      var shared = require_shared();
      var uid = require_uid();
      var keys = shared("keys");
      module.exports = function(key) {
        return keys[key] || (keys[key] = uid(key));
      };
    }
  });

  // node_modules/core-js-pure/internals/hidden-keys.js
  var require_hidden_keys = __commonJS({
    "node_modules/core-js-pure/internals/hidden-keys.js"(exports, module) {
      "use strict";
      module.exports = {};
    }
  });

  // node_modules/core-js-pure/internals/internal-state.js
  var require_internal_state = __commonJS({
    "node_modules/core-js-pure/internals/internal-state.js"(exports, module) {
      "use strict";
      var NATIVE_WEAK_MAP = require_weak_map_basic_detection();
      var globalThis2 = require_global_this();
      var isObject = require_is_object();
      var createNonEnumerableProperty = require_create_non_enumerable_property();
      var hasOwn = require_has_own_property();
      var shared = require_shared_store();
      var sharedKey = require_shared_key();
      var hiddenKeys = require_hidden_keys();
      var OBJECT_ALREADY_INITIALIZED = "Object already initialized";
      var TypeError2 = globalThis2.TypeError;
      var WeakMap = globalThis2.WeakMap;
      var set;
      var get;
      var has;
      var enforce = function(it) {
        return has(it) ? get(it) : set(it, {});
      };
      var getterFor = function(TYPE) {
        return function(it) {
          var state;
          if (!isObject(it) || (state = get(it)).type !== TYPE) {
            throw new TypeError2("Incompatible receiver, " + TYPE + " required");
          }
          return state;
        };
      };
      if (NATIVE_WEAK_MAP || shared.state) {
        store = shared.state || (shared.state = new WeakMap());
        store.get = store.get;
        store.has = store.has;
        store.set = store.set;
        set = function(it, metadata) {
          if (store.has(it)) throw new TypeError2(OBJECT_ALREADY_INITIALIZED);
          metadata.facade = it;
          store.set(it, metadata);
          return metadata;
        };
        get = function(it) {
          return store.get(it) || {};
        };
        has = function(it) {
          return store.has(it);
        };
      } else {
        STATE = sharedKey("state");
        hiddenKeys[STATE] = true;
        set = function(it, metadata) {
          if (hasOwn(it, STATE)) throw new TypeError2(OBJECT_ALREADY_INITIALIZED);
          metadata.facade = it;
          createNonEnumerableProperty(it, STATE, metadata);
          return metadata;
        };
        get = function(it) {
          return hasOwn(it, STATE) ? it[STATE] : {};
        };
        has = function(it) {
          return hasOwn(it, STATE);
        };
      }
      var store;
      var STATE;
      module.exports = {
        set,
        get,
        has,
        enforce,
        getterFor
      };
    }
  });

  // node_modules/core-js-pure/internals/function-name.js
  var require_function_name = __commonJS({
    "node_modules/core-js-pure/internals/function-name.js"(exports, module) {
      "use strict";
      var DESCRIPTORS = require_descriptors();
      var hasOwn = require_has_own_property();
      var FunctionPrototype = Function.prototype;
      var getDescriptor = DESCRIPTORS && Object.getOwnPropertyDescriptor;
      var EXISTS = hasOwn(FunctionPrototype, "name");
      var PROPER = EXISTS && function something() {
      }.name === "something";
      var CONFIGURABLE = EXISTS && (!DESCRIPTORS || DESCRIPTORS && getDescriptor(FunctionPrototype, "name").configurable);
      module.exports = {
        EXISTS,
        PROPER,
        CONFIGURABLE
      };
    }
  });

  // node_modules/core-js-pure/internals/array-includes.js
  var require_array_includes = __commonJS({
    "node_modules/core-js-pure/internals/array-includes.js"(exports, module) {
      "use strict";
      var toIndexedObject = require_to_indexed_object();
      var toAbsoluteIndex = require_to_absolute_index();
      var lengthOfArrayLike = require_length_of_array_like();
      var createMethod = function(IS_INCLUDES) {
        return function($this, el, fromIndex) {
          var O = toIndexedObject($this);
          var length = lengthOfArrayLike(O);
          if (length === 0) return !IS_INCLUDES && -1;
          var index = toAbsoluteIndex(fromIndex, length);
          var value;
          if (IS_INCLUDES && el !== el) while (length > index) {
            value = O[index++];
            if (value !== value) return true;
          }
          else for (; length > index; index++) {
            if ((IS_INCLUDES || index in O) && O[index] === el) return IS_INCLUDES || index || 0;
          }
          return !IS_INCLUDES && -1;
        };
      };
      module.exports = {
        // `Array.prototype.includes` method
        // https://tc39.es/ecma262/#sec-array.prototype.includes
        includes: createMethod(true),
        // `Array.prototype.indexOf` method
        // https://tc39.es/ecma262/#sec-array.prototype.indexof
        indexOf: createMethod(false)
      };
    }
  });

  // node_modules/core-js-pure/internals/object-keys-internal.js
  var require_object_keys_internal = __commonJS({
    "node_modules/core-js-pure/internals/object-keys-internal.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var hasOwn = require_has_own_property();
      var toIndexedObject = require_to_indexed_object();
      var indexOf = require_array_includes().indexOf;
      var hiddenKeys = require_hidden_keys();
      var push = uncurryThis([].push);
      module.exports = function(object, names) {
        var O = toIndexedObject(object);
        var i = 0;
        var result = [];
        var key;
        for (key in O) !hasOwn(hiddenKeys, key) && hasOwn(O, key) && push(result, key);
        while (names.length > i) if (hasOwn(O, key = names[i++])) {
          ~indexOf(result, key) || push(result, key);
        }
        return result;
      };
    }
  });

  // node_modules/core-js-pure/internals/enum-bug-keys.js
  var require_enum_bug_keys = __commonJS({
    "node_modules/core-js-pure/internals/enum-bug-keys.js"(exports, module) {
      "use strict";
      module.exports = [
        "constructor",
        "hasOwnProperty",
        "isPrototypeOf",
        "propertyIsEnumerable",
        "toLocaleString",
        "toString",
        "valueOf"
      ];
    }
  });

  // node_modules/core-js-pure/internals/object-keys.js
  var require_object_keys = __commonJS({
    "node_modules/core-js-pure/internals/object-keys.js"(exports, module) {
      "use strict";
      var internalObjectKeys = require_object_keys_internal();
      var enumBugKeys = require_enum_bug_keys();
      module.exports = Object.keys || function keys(O) {
        return internalObjectKeys(O, enumBugKeys);
      };
    }
  });

  // node_modules/core-js-pure/internals/object-define-properties.js
  var require_object_define_properties = __commonJS({
    "node_modules/core-js-pure/internals/object-define-properties.js"(exports) {
      "use strict";
      var DESCRIPTORS = require_descriptors();
      var V8_PROTOTYPE_DEFINE_BUG = require_v8_prototype_define_bug();
      var definePropertyModule = require_object_define_property();
      var anObject = require_an_object();
      var toIndexedObject = require_to_indexed_object();
      var objectKeys = require_object_keys();
      exports.f = DESCRIPTORS && !V8_PROTOTYPE_DEFINE_BUG ? Object.defineProperties : function defineProperties(O, Properties) {
        anObject(O);
        var props = toIndexedObject(Properties);
        var keys = objectKeys(Properties);
        var length = keys.length;
        var index = 0;
        var key;
        while (length > index) definePropertyModule.f(O, key = keys[index++], props[key]);
        return O;
      };
    }
  });

  // node_modules/core-js-pure/internals/html.js
  var require_html = __commonJS({
    "node_modules/core-js-pure/internals/html.js"(exports, module) {
      "use strict";
      var getBuiltIn = require_get_built_in();
      module.exports = getBuiltIn("document", "documentElement");
    }
  });

  // node_modules/core-js-pure/internals/object-create.js
  var require_object_create = __commonJS({
    "node_modules/core-js-pure/internals/object-create.js"(exports, module) {
      "use strict";
      var anObject = require_an_object();
      var definePropertiesModule = require_object_define_properties();
      var enumBugKeys = require_enum_bug_keys();
      var hiddenKeys = require_hidden_keys();
      var html = require_html();
      var documentCreateElement = require_document_create_element();
      var sharedKey = require_shared_key();
      var GT = ">";
      var LT = "<";
      var PROTOTYPE = "prototype";
      var SCRIPT = "script";
      var IE_PROTO = sharedKey("IE_PROTO");
      var EmptyConstructor = function() {
      };
      var scriptTag = function(content) {
        return LT + SCRIPT + GT + content + LT + "/" + SCRIPT + GT;
      };
      var NullProtoObjectViaActiveX = function(activeXDocument2) {
        activeXDocument2.write(scriptTag(""));
        activeXDocument2.close();
        var temp = activeXDocument2.parentWindow.Object;
        activeXDocument2 = null;
        return temp;
      };
      var NullProtoObjectViaIFrame = function() {
        var iframe = documentCreateElement("iframe");
        var JS = "java" + SCRIPT + ":";
        var iframeDocument;
        iframe.style.display = "none";
        html.appendChild(iframe);
        iframe.src = String(JS);
        iframeDocument = iframe.contentWindow.document;
        iframeDocument.open();
        iframeDocument.write(scriptTag("document.F=Object"));
        iframeDocument.close();
        return iframeDocument.F;
      };
      var activeXDocument;
      var NullProtoObject = function() {
        try {
          activeXDocument = new ActiveXObject("htmlfile");
        } catch (error) {
        }
        NullProtoObject = typeof document != "undefined" ? document.domain && activeXDocument ? NullProtoObjectViaActiveX(activeXDocument) : NullProtoObjectViaIFrame() : NullProtoObjectViaActiveX(activeXDocument);
        var length = enumBugKeys.length;
        while (length--) delete NullProtoObject[PROTOTYPE][enumBugKeys[length]];
        return NullProtoObject();
      };
      hiddenKeys[IE_PROTO] = true;
      module.exports = Object.create || function create(O, Properties) {
        var result;
        if (O !== null) {
          EmptyConstructor[PROTOTYPE] = anObject(O);
          result = new EmptyConstructor();
          EmptyConstructor[PROTOTYPE] = null;
          result[IE_PROTO] = O;
        } else result = NullProtoObject();
        return Properties === void 0 ? result : definePropertiesModule.f(result, Properties);
      };
    }
  });

  // node_modules/core-js-pure/internals/correct-prototype-getter.js
  var require_correct_prototype_getter = __commonJS({
    "node_modules/core-js-pure/internals/correct-prototype-getter.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      module.exports = !fails(function() {
        function F() {
        }
        F.prototype.constructor = null;
        return Object.getPrototypeOf(new F()) !== F.prototype;
      });
    }
  });

  // node_modules/core-js-pure/internals/object-get-prototype-of.js
  var require_object_get_prototype_of = __commonJS({
    "node_modules/core-js-pure/internals/object-get-prototype-of.js"(exports, module) {
      "use strict";
      var hasOwn = require_has_own_property();
      var isCallable = require_is_callable();
      var toObject = require_to_object();
      var sharedKey = require_shared_key();
      var CORRECT_PROTOTYPE_GETTER = require_correct_prototype_getter();
      var IE_PROTO = sharedKey("IE_PROTO");
      var $Object = Object;
      var ObjectPrototype = $Object.prototype;
      module.exports = CORRECT_PROTOTYPE_GETTER ? $Object.getPrototypeOf : function(O) {
        var object = toObject(O);
        if (hasOwn(object, IE_PROTO)) return object[IE_PROTO];
        var constructor = object.constructor;
        if (isCallable(constructor) && object instanceof constructor) {
          return constructor.prototype;
        }
        return object instanceof $Object ? ObjectPrototype : null;
      };
    }
  });

  // node_modules/core-js-pure/internals/define-built-in.js
  var require_define_built_in = __commonJS({
    "node_modules/core-js-pure/internals/define-built-in.js"(exports, module) {
      "use strict";
      var createNonEnumerableProperty = require_create_non_enumerable_property();
      module.exports = function(target, key, value, options) {
        if (options && options.enumerable) target[key] = value;
        else createNonEnumerableProperty(target, key, value);
        return target;
      };
    }
  });

  // node_modules/core-js-pure/internals/iterators-core.js
  var require_iterators_core = __commonJS({
    "node_modules/core-js-pure/internals/iterators-core.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      var isCallable = require_is_callable();
      var isObject = require_is_object();
      var create = require_object_create();
      var getPrototypeOf = require_object_get_prototype_of();
      var defineBuiltIn = require_define_built_in();
      var wellKnownSymbol = require_well_known_symbol();
      var IS_PURE = require_is_pure();
      var ITERATOR = wellKnownSymbol("iterator");
      var BUGGY_SAFARI_ITERATORS = false;
      var IteratorPrototype;
      var PrototypeOfArrayIteratorPrototype;
      var arrayIterator;
      if ([].keys) {
        arrayIterator = [].keys();
        if (!("next" in arrayIterator)) BUGGY_SAFARI_ITERATORS = true;
        else {
          PrototypeOfArrayIteratorPrototype = getPrototypeOf(getPrototypeOf(arrayIterator));
          if (PrototypeOfArrayIteratorPrototype !== Object.prototype) IteratorPrototype = PrototypeOfArrayIteratorPrototype;
        }
      }
      var NEW_ITERATOR_PROTOTYPE = !isObject(IteratorPrototype) || fails(function() {
        var test = {};
        return IteratorPrototype[ITERATOR].call(test) !== test;
      });
      if (NEW_ITERATOR_PROTOTYPE) IteratorPrototype = {};
      else if (IS_PURE) IteratorPrototype = create(IteratorPrototype);
      if (!isCallable(IteratorPrototype[ITERATOR])) {
        defineBuiltIn(IteratorPrototype, ITERATOR, function() {
          return this;
        });
      }
      module.exports = {
        IteratorPrototype,
        BUGGY_SAFARI_ITERATORS
      };
    }
  });

  // node_modules/core-js-pure/internals/object-to-string.js
  var require_object_to_string = __commonJS({
    "node_modules/core-js-pure/internals/object-to-string.js"(exports, module) {
      "use strict";
      var TO_STRING_TAG_SUPPORT = require_to_string_tag_support();
      var classof = require_classof();
      module.exports = TO_STRING_TAG_SUPPORT ? {}.toString : function toString() {
        return "[object " + classof(this) + "]";
      };
    }
  });

  // node_modules/core-js-pure/internals/set-to-string-tag.js
  var require_set_to_string_tag = __commonJS({
    "node_modules/core-js-pure/internals/set-to-string-tag.js"(exports, module) {
      "use strict";
      var TO_STRING_TAG_SUPPORT = require_to_string_tag_support();
      var defineProperty = require_object_define_property().f;
      var createNonEnumerableProperty = require_create_non_enumerable_property();
      var hasOwn = require_has_own_property();
      var toString = require_object_to_string();
      var wellKnownSymbol = require_well_known_symbol();
      var TO_STRING_TAG = wellKnownSymbol("toStringTag");
      module.exports = function(it, TAG, STATIC, SET_METHOD) {
        var target = STATIC ? it : it && it.prototype;
        if (target) {
          if (!hasOwn(target, TO_STRING_TAG)) {
            defineProperty(target, TO_STRING_TAG, { configurable: true, value: TAG });
          }
          if (SET_METHOD && !TO_STRING_TAG_SUPPORT) {
            createNonEnumerableProperty(target, "toString", toString);
          }
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/iterators.js
  var require_iterators = __commonJS({
    "node_modules/core-js-pure/internals/iterators.js"(exports, module) {
      "use strict";
      module.exports = {};
    }
  });

  // node_modules/core-js-pure/internals/iterator-create-constructor.js
  var require_iterator_create_constructor = __commonJS({
    "node_modules/core-js-pure/internals/iterator-create-constructor.js"(exports, module) {
      "use strict";
      var IteratorPrototype = require_iterators_core().IteratorPrototype;
      var create = require_object_create();
      var createPropertyDescriptor = require_create_property_descriptor();
      var setToStringTag = require_set_to_string_tag();
      var Iterators = require_iterators();
      var returnThis = function() {
        return this;
      };
      module.exports = function(IteratorConstructor, NAME, next, ENUMERABLE_NEXT) {
        var TO_STRING_TAG = NAME + " Iterator";
        IteratorConstructor.prototype = create(IteratorPrototype, { next: createPropertyDescriptor(+!ENUMERABLE_NEXT, next) });
        setToStringTag(IteratorConstructor, TO_STRING_TAG, false, true);
        Iterators[TO_STRING_TAG] = returnThis;
        return IteratorConstructor;
      };
    }
  });

  // node_modules/core-js-pure/internals/function-uncurry-this-accessor.js
  var require_function_uncurry_this_accessor = __commonJS({
    "node_modules/core-js-pure/internals/function-uncurry-this-accessor.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var aCallable = require_a_callable();
      module.exports = function(object, key, method) {
        try {
          return uncurryThis(aCallable(Object.getOwnPropertyDescriptor(object, key)[method]));
        } catch (error) {
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/is-possible-prototype.js
  var require_is_possible_prototype = __commonJS({
    "node_modules/core-js-pure/internals/is-possible-prototype.js"(exports, module) {
      "use strict";
      var isObject = require_is_object();
      module.exports = function(argument) {
        return isObject(argument) || argument === null;
      };
    }
  });

  // node_modules/core-js-pure/internals/a-possible-prototype.js
  var require_a_possible_prototype = __commonJS({
    "node_modules/core-js-pure/internals/a-possible-prototype.js"(exports, module) {
      "use strict";
      var isPossiblePrototype = require_is_possible_prototype();
      var $String = String;
      var $TypeError = TypeError;
      module.exports = function(argument) {
        if (isPossiblePrototype(argument)) return argument;
        throw new $TypeError("Can't set " + $String(argument) + " as a prototype");
      };
    }
  });

  // node_modules/core-js-pure/internals/object-set-prototype-of.js
  var require_object_set_prototype_of = __commonJS({
    "node_modules/core-js-pure/internals/object-set-prototype-of.js"(exports, module) {
      "use strict";
      var uncurryThisAccessor = require_function_uncurry_this_accessor();
      var isObject = require_is_object();
      var requireObjectCoercible = require_require_object_coercible();
      var aPossiblePrototype = require_a_possible_prototype();
      module.exports = Object.setPrototypeOf || ("__proto__" in {} ? (function() {
        var CORRECT_SETTER = false;
        var test = {};
        var setter;
        try {
          setter = uncurryThisAccessor(Object.prototype, "__proto__", "set");
          setter(test, []);
          CORRECT_SETTER = test instanceof Array;
        } catch (error) {
        }
        return function setPrototypeOf(O, proto) {
          requireObjectCoercible(O);
          aPossiblePrototype(proto);
          if (!isObject(O)) return O;
          if (CORRECT_SETTER) setter(O, proto);
          else O.__proto__ = proto;
          return O;
        };
      })() : void 0);
    }
  });

  // node_modules/core-js-pure/internals/iterator-define.js
  var require_iterator_define = __commonJS({
    "node_modules/core-js-pure/internals/iterator-define.js"(exports, module) {
      "use strict";
      var $ = require_export();
      var call = require_function_call();
      var IS_PURE = require_is_pure();
      var FunctionName = require_function_name();
      var isCallable = require_is_callable();
      var createIteratorConstructor = require_iterator_create_constructor();
      var getPrototypeOf = require_object_get_prototype_of();
      var setPrototypeOf = require_object_set_prototype_of();
      var setToStringTag = require_set_to_string_tag();
      var createNonEnumerableProperty = require_create_non_enumerable_property();
      var defineBuiltIn = require_define_built_in();
      var wellKnownSymbol = require_well_known_symbol();
      var Iterators = require_iterators();
      var IteratorsCore = require_iterators_core();
      var PROPER_FUNCTION_NAME = FunctionName.PROPER;
      var CONFIGURABLE_FUNCTION_NAME = FunctionName.CONFIGURABLE;
      var IteratorPrototype = IteratorsCore.IteratorPrototype;
      var BUGGY_SAFARI_ITERATORS = IteratorsCore.BUGGY_SAFARI_ITERATORS;
      var ITERATOR = wellKnownSymbol("iterator");
      var KEYS = "keys";
      var VALUES = "values";
      var ENTRIES = "entries";
      var returnThis = function() {
        return this;
      };
      module.exports = function(Iterable, NAME, IteratorConstructor, next, DEFAULT, IS_SET, FORCED) {
        createIteratorConstructor(IteratorConstructor, NAME, next);
        var getIterationMethod = function(KIND) {
          if (KIND === DEFAULT && defaultIterator) return defaultIterator;
          if (!BUGGY_SAFARI_ITERATORS && KIND && KIND in IterablePrototype) return IterablePrototype[KIND];
          switch (KIND) {
            case KEYS:
              return function keys() {
                return new IteratorConstructor(this, KIND);
              };
            case VALUES:
              return function values() {
                return new IteratorConstructor(this, KIND);
              };
            case ENTRIES:
              return function entries() {
                return new IteratorConstructor(this, KIND);
              };
          }
          return function() {
            return new IteratorConstructor(this);
          };
        };
        var TO_STRING_TAG = NAME + " Iterator";
        var INCORRECT_VALUES_NAME = false;
        var IterablePrototype = Iterable.prototype;
        var nativeIterator = IterablePrototype[ITERATOR] || IterablePrototype["@@iterator"] || DEFAULT && IterablePrototype[DEFAULT];
        var defaultIterator = !BUGGY_SAFARI_ITERATORS && nativeIterator || getIterationMethod(DEFAULT);
        var anyNativeIterator = NAME === "Array" ? IterablePrototype.entries || nativeIterator : nativeIterator;
        var CurrentIteratorPrototype, methods, KEY;
        if (anyNativeIterator) {
          CurrentIteratorPrototype = getPrototypeOf(anyNativeIterator.call(new Iterable()));
          if (CurrentIteratorPrototype !== Object.prototype && CurrentIteratorPrototype.next) {
            if (!IS_PURE && getPrototypeOf(CurrentIteratorPrototype) !== IteratorPrototype) {
              if (setPrototypeOf) {
                setPrototypeOf(CurrentIteratorPrototype, IteratorPrototype);
              } else if (!isCallable(CurrentIteratorPrototype[ITERATOR])) {
                defineBuiltIn(CurrentIteratorPrototype, ITERATOR, returnThis);
              }
            }
            setToStringTag(CurrentIteratorPrototype, TO_STRING_TAG, true, true);
            if (IS_PURE) Iterators[TO_STRING_TAG] = returnThis;
          }
        }
        if (PROPER_FUNCTION_NAME && DEFAULT === VALUES && nativeIterator && nativeIterator.name !== VALUES) {
          if (!IS_PURE && CONFIGURABLE_FUNCTION_NAME) {
            createNonEnumerableProperty(IterablePrototype, "name", VALUES);
          } else {
            INCORRECT_VALUES_NAME = true;
            defaultIterator = function values() {
              return call(nativeIterator, this);
            };
          }
        }
        if (DEFAULT) {
          methods = {
            values: getIterationMethod(VALUES),
            keys: IS_SET ? defaultIterator : getIterationMethod(KEYS),
            entries: getIterationMethod(ENTRIES)
          };
          if (FORCED) for (KEY in methods) {
            if (BUGGY_SAFARI_ITERATORS || INCORRECT_VALUES_NAME || !(KEY in IterablePrototype)) {
              defineBuiltIn(IterablePrototype, KEY, methods[KEY]);
            }
          }
          else $({ target: NAME, proto: true, forced: BUGGY_SAFARI_ITERATORS || INCORRECT_VALUES_NAME }, methods);
        }
        if ((!IS_PURE || FORCED) && IterablePrototype[ITERATOR] !== defaultIterator) {
          defineBuiltIn(IterablePrototype, ITERATOR, defaultIterator, { name: DEFAULT });
        }
        Iterators[NAME] = defaultIterator;
        return methods;
      };
    }
  });

  // node_modules/core-js-pure/internals/create-iter-result-object.js
  var require_create_iter_result_object = __commonJS({
    "node_modules/core-js-pure/internals/create-iter-result-object.js"(exports, module) {
      "use strict";
      module.exports = function(value, done) {
        return { value, done };
      };
    }
  });

  // node_modules/core-js-pure/modules/es.string.iterator.js
  var require_es_string_iterator = __commonJS({
    "node_modules/core-js-pure/modules/es.string.iterator.js"() {
      "use strict";
      var charAt = require_string_multibyte().charAt;
      var toString = require_to_string();
      var InternalStateModule = require_internal_state();
      var defineIterator = require_iterator_define();
      var createIterResultObject = require_create_iter_result_object();
      var STRING_ITERATOR = "String Iterator";
      var setInternalState = InternalStateModule.set;
      var getInternalState = InternalStateModule.getterFor(STRING_ITERATOR);
      defineIterator(String, "String", function(iterated) {
        setInternalState(this, {
          type: STRING_ITERATOR,
          string: toString(iterated),
          index: 0
        });
      }, function next() {
        var state = getInternalState(this);
        var string = state.string;
        var index = state.index;
        var point;
        if (index >= string.length) return createIterResultObject(void 0, true);
        point = charAt(string, index);
        state.index += point.length;
        return createIterResultObject(point, false);
      });
    }
  });

  // node_modules/core-js-pure/internals/iterator-close.js
  var require_iterator_close = __commonJS({
    "node_modules/core-js-pure/internals/iterator-close.js"(exports, module) {
      "use strict";
      var call = require_function_call();
      var anObject = require_an_object();
      var getMethod = require_get_method();
      module.exports = function(iterator, kind, value) {
        var innerResult, innerError;
        anObject(iterator);
        try {
          innerResult = getMethod(iterator, "return");
          if (!innerResult) {
            if (kind === "throw") throw value;
            return value;
          }
          innerResult = call(innerResult, iterator);
        } catch (error) {
          innerError = true;
          innerResult = error;
        }
        if (kind === "throw") throw value;
        if (innerError) throw innerResult;
        anObject(innerResult);
        return value;
      };
    }
  });

  // node_modules/core-js-pure/internals/call-with-safe-iteration-closing.js
  var require_call_with_safe_iteration_closing = __commonJS({
    "node_modules/core-js-pure/internals/call-with-safe-iteration-closing.js"(exports, module) {
      "use strict";
      var anObject = require_an_object();
      var iteratorClose = require_iterator_close();
      module.exports = function(iterator, fn, value, ENTRIES) {
        try {
          return ENTRIES ? fn(anObject(value)[0], value[1]) : fn(value);
        } catch (error) {
          iteratorClose(iterator, "throw", error);
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/is-array-iterator-method.js
  var require_is_array_iterator_method = __commonJS({
    "node_modules/core-js-pure/internals/is-array-iterator-method.js"(exports, module) {
      "use strict";
      var wellKnownSymbol = require_well_known_symbol();
      var Iterators = require_iterators();
      var ITERATOR = wellKnownSymbol("iterator");
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        return it !== void 0 && (Iterators.Array === it || ArrayPrototype[ITERATOR] === it);
      };
    }
  });

  // node_modules/core-js-pure/internals/get-iterator-method.js
  var require_get_iterator_method = __commonJS({
    "node_modules/core-js-pure/internals/get-iterator-method.js"(exports, module) {
      "use strict";
      var classof = require_classof();
      var getMethod = require_get_method();
      var isNullOrUndefined = require_is_null_or_undefined();
      var Iterators = require_iterators();
      var wellKnownSymbol = require_well_known_symbol();
      var ITERATOR = wellKnownSymbol("iterator");
      module.exports = function(it) {
        if (!isNullOrUndefined(it)) return getMethod(it, ITERATOR) || getMethod(it, "@@iterator") || Iterators[classof(it)];
      };
    }
  });

  // node_modules/core-js-pure/internals/get-iterator.js
  var require_get_iterator = __commonJS({
    "node_modules/core-js-pure/internals/get-iterator.js"(exports, module) {
      "use strict";
      var call = require_function_call();
      var aCallable = require_a_callable();
      var anObject = require_an_object();
      var tryToString = require_try_to_string();
      var getIteratorMethod = require_get_iterator_method();
      var $TypeError = TypeError;
      module.exports = function(argument, usingIterator) {
        var iteratorMethod = arguments.length < 2 ? getIteratorMethod(argument) : usingIterator;
        if (aCallable(iteratorMethod)) return anObject(call(iteratorMethod, argument));
        throw new $TypeError(tryToString(argument) + " is not iterable");
      };
    }
  });

  // node_modules/core-js-pure/internals/array-from.js
  var require_array_from = __commonJS({
    "node_modules/core-js-pure/internals/array-from.js"(exports, module) {
      "use strict";
      var bind = require_function_bind_context();
      var call = require_function_call();
      var toObject = require_to_object();
      var callWithSafeIterationClosing = require_call_with_safe_iteration_closing();
      var isArrayIteratorMethod = require_is_array_iterator_method();
      var isConstructor = require_is_constructor();
      var lengthOfArrayLike = require_length_of_array_like();
      var createProperty = require_create_property();
      var setArrayLength = require_array_set_length();
      var getIterator = require_get_iterator();
      var getIteratorMethod = require_get_iterator_method();
      var iteratorClose = require_iterator_close();
      var $Array = Array;
      module.exports = function from(arrayLike) {
        var IS_CONSTRUCTOR = isConstructor(this);
        var argumentsLength = arguments.length;
        var mapfn = argumentsLength > 1 ? arguments[1] : void 0;
        var mapping = mapfn !== void 0;
        if (mapping) mapfn = bind(mapfn, argumentsLength > 2 ? arguments[2] : void 0);
        var O = toObject(arrayLike);
        var iteratorMethod = getIteratorMethod(O);
        var index = 0;
        var length, result, step, iterator, next, value;
        if (iteratorMethod && !(this === $Array && isArrayIteratorMethod(iteratorMethod))) {
          result = IS_CONSTRUCTOR ? new this() : [];
          iterator = getIterator(O, iteratorMethod);
          next = iterator.next;
          for (; !(step = call(next, iterator)).done; index++) {
            value = mapping ? callWithSafeIterationClosing(iterator, mapfn, [step.value, index], true) : step.value;
            try {
              createProperty(result, index, value);
            } catch (error) {
              iteratorClose(iterator, "throw", error);
            }
          }
        } else {
          length = lengthOfArrayLike(O);
          result = IS_CONSTRUCTOR ? new this(length) : $Array(length);
          for (; length > index; index++) {
            value = mapping ? mapfn(O[index], index) : O[index];
            createProperty(result, index, value);
          }
        }
        setArrayLength(result, index);
        return result;
      };
    }
  });

  // node_modules/core-js-pure/internals/check-correctness-of-iteration.js
  var require_check_correctness_of_iteration = __commonJS({
    "node_modules/core-js-pure/internals/check-correctness-of-iteration.js"(exports, module) {
      "use strict";
      var wellKnownSymbol = require_well_known_symbol();
      var ITERATOR = wellKnownSymbol("iterator");
      var SAFE_CLOSING = false;
      try {
        called = 0;
        iteratorWithReturn = {
          next: function() {
            return { done: !!called++ };
          },
          "return": function() {
            SAFE_CLOSING = true;
          }
        };
        iteratorWithReturn[ITERATOR] = function() {
          return this;
        };
        Array.from(iteratorWithReturn, function() {
          throw 2;
        });
      } catch (error) {
      }
      var called;
      var iteratorWithReturn;
      module.exports = function(exec, SKIP_CLOSING) {
        try {
          if (!SKIP_CLOSING && !SAFE_CLOSING) return false;
        } catch (error) {
          return false;
        }
        var ITERATION_SUPPORT = false;
        try {
          var object = {};
          object[ITERATOR] = function() {
            return {
              next: function() {
                return { done: ITERATION_SUPPORT = true };
              }
            };
          };
          exec(object);
        } catch (error) {
        }
        return ITERATION_SUPPORT;
      };
    }
  });

  // node_modules/core-js-pure/modules/es.array.from.js
  var require_es_array_from = __commonJS({
    "node_modules/core-js-pure/modules/es.array.from.js"() {
      "use strict";
      var $ = require_export();
      var from = require_array_from();
      var checkCorrectnessOfIteration = require_check_correctness_of_iteration();
      var INCORRECT_ITERATION = !checkCorrectnessOfIteration(function(iterable) {
        Array.from(iterable);
      });
      $({ target: "Array", stat: true, forced: INCORRECT_ITERATION }, {
        from
      });
    }
  });

  // node_modules/core-js-pure/es/array/from.js
  var require_from = __commonJS({
    "node_modules/core-js-pure/es/array/from.js"(exports, module) {
      "use strict";
      require_es_string_iterator();
      require_es_array_from();
      var path = require_path();
      module.exports = path.Array.from;
    }
  });

  // node_modules/core-js-pure/stable/array/from.js
  var require_from2 = __commonJS({
    "node_modules/core-js-pure/stable/array/from.js"(exports, module) {
      "use strict";
      var parent = require_from();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/array/from.js
  var require_from3 = __commonJS({
    "node_modules/core-js-pure/actual/array/from.js"(exports, module) {
      "use strict";
      var parent = require_from2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/array/from.js
  var require_from4 = __commonJS({
    "node_modules/core-js-pure/full/array/from.js"(exports, module) {
      "use strict";
      var parent = require_from3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/array/from.js
  var require_from5 = __commonJS({
    "node_modules/core-js-pure/features/array/from.js"(exports, module) {
      "use strict";
      module.exports = require_from4();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/array/from.js
  var require_from6 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/array/from.js"(exports, module) {
      module.exports = require_from5();
    }
  });

  // node_modules/core-js-pure/internals/does-not-exceed-safe-integer.js
  var require_does_not_exceed_safe_integer = __commonJS({
    "node_modules/core-js-pure/internals/does-not-exceed-safe-integer.js"(exports, module) {
      "use strict";
      var $TypeError = TypeError;
      var MAX_SAFE_INTEGER = 9007199254740991;
      module.exports = function(it) {
        if (it > MAX_SAFE_INTEGER) throw new $TypeError("Maximum allowed index exceeded");
        return it;
      };
    }
  });

  // node_modules/core-js-pure/internals/array-species-constructor.js
  var require_array_species_constructor = __commonJS({
    "node_modules/core-js-pure/internals/array-species-constructor.js"(exports, module) {
      "use strict";
      var isArray = require_is_array();
      var isConstructor = require_is_constructor();
      var isObject = require_is_object();
      var wellKnownSymbol = require_well_known_symbol();
      var SPECIES = wellKnownSymbol("species");
      var $Array = Array;
      module.exports = function(originalArray) {
        var C;
        if (isArray(originalArray)) {
          C = originalArray.constructor;
          if (isConstructor(C) && (C === $Array || isArray(C.prototype))) C = void 0;
          else if (isObject(C)) {
            C = C[SPECIES];
            if (C === null) C = void 0;
          }
        }
        return C === void 0 ? $Array : C;
      };
    }
  });

  // node_modules/core-js-pure/internals/array-species-create.js
  var require_array_species_create = __commonJS({
    "node_modules/core-js-pure/internals/array-species-create.js"(exports, module) {
      "use strict";
      var arraySpeciesConstructor = require_array_species_constructor();
      module.exports = function(originalArray, length) {
        return new (arraySpeciesConstructor(originalArray))(length === 0 ? 0 : length);
      };
    }
  });

  // node_modules/core-js-pure/modules/es.array.concat.js
  var require_es_array_concat = __commonJS({
    "node_modules/core-js-pure/modules/es.array.concat.js"() {
      "use strict";
      var $ = require_export();
      var fails = require_fails();
      var isArray = require_is_array();
      var isObject = require_is_object();
      var toObject = require_to_object();
      var lengthOfArrayLike = require_length_of_array_like();
      var doesNotExceedSafeInteger = require_does_not_exceed_safe_integer();
      var createProperty = require_create_property();
      var setArrayLength = require_array_set_length();
      var arraySpeciesCreate = require_array_species_create();
      var arrayMethodHasSpeciesSupport = require_array_method_has_species_support();
      var wellKnownSymbol = require_well_known_symbol();
      var V8_VERSION = require_environment_v8_version();
      var IS_CONCAT_SPREADABLE = wellKnownSymbol("isConcatSpreadable");
      var IS_CONCAT_SPREADABLE_SUPPORT = V8_VERSION >= 51 || !fails(function() {
        var array = [];
        array[IS_CONCAT_SPREADABLE] = false;
        return array.concat()[0] !== array;
      });
      var isConcatSpreadable = function(O) {
        if (!isObject(O)) return false;
        var spreadable = O[IS_CONCAT_SPREADABLE];
        return spreadable !== void 0 ? !!spreadable : isArray(O);
      };
      var FORCED = !IS_CONCAT_SPREADABLE_SUPPORT || !arrayMethodHasSpeciesSupport("concat");
      $({ target: "Array", proto: true, arity: 1, forced: FORCED }, {
        // eslint-disable-next-line no-unused-vars -- required for `.length`
        concat: function concat(arg) {
          var O = toObject(this);
          var A = arraySpeciesCreate(O, 0);
          var n = 0;
          var i, k, length, len, E;
          for (i = -1, length = arguments.length; i < length; i++) {
            E = i === -1 ? O : arguments[i];
            if (isConcatSpreadable(E)) {
              len = lengthOfArrayLike(E);
              doesNotExceedSafeInteger(n + len);
              for (k = 0; k < len; k++, n++) if (k in E) createProperty(A, n, E[k]);
            } else {
              doesNotExceedSafeInteger(n + 1);
              createProperty(A, n++, E);
            }
          }
          setArrayLength(A, n);
          return A;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.object.to-string.js
  var require_es_object_to_string = __commonJS({
    "node_modules/core-js-pure/modules/es.object.to-string.js"() {
    }
  });

  // node_modules/core-js-pure/internals/object-get-own-property-names.js
  var require_object_get_own_property_names = __commonJS({
    "node_modules/core-js-pure/internals/object-get-own-property-names.js"(exports) {
      "use strict";
      var internalObjectKeys = require_object_keys_internal();
      var enumBugKeys = require_enum_bug_keys();
      var hiddenKeys = enumBugKeys.concat("length", "prototype");
      exports.f = Object.getOwnPropertyNames || function getOwnPropertyNames(O) {
        return internalObjectKeys(O, hiddenKeys);
      };
    }
  });

  // node_modules/core-js-pure/internals/object-get-own-property-names-external.js
  var require_object_get_own_property_names_external = __commonJS({
    "node_modules/core-js-pure/internals/object-get-own-property-names-external.js"(exports, module) {
      "use strict";
      var classof = require_classof_raw();
      var toIndexedObject = require_to_indexed_object();
      var $getOwnPropertyNames = require_object_get_own_property_names().f;
      var arraySlice = require_array_slice();
      var windowNames = typeof window == "object" && window && Object.getOwnPropertyNames ? Object.getOwnPropertyNames(window) : [];
      var getWindowNames = function(it) {
        try {
          return $getOwnPropertyNames(it);
        } catch (error) {
          return arraySlice(windowNames);
        }
      };
      module.exports.f = function getOwnPropertyNames(it) {
        return windowNames && classof(it) === "Window" ? getWindowNames(it) : $getOwnPropertyNames(toIndexedObject(it));
      };
    }
  });

  // node_modules/core-js-pure/internals/object-get-own-property-symbols.js
  var require_object_get_own_property_symbols = __commonJS({
    "node_modules/core-js-pure/internals/object-get-own-property-symbols.js"(exports) {
      "use strict";
      exports.f = Object.getOwnPropertySymbols;
    }
  });

  // node_modules/core-js-pure/internals/define-built-in-accessor.js
  var require_define_built_in_accessor = __commonJS({
    "node_modules/core-js-pure/internals/define-built-in-accessor.js"(exports, module) {
      "use strict";
      var defineProperty = require_object_define_property();
      module.exports = function(target, name, descriptor) {
        return defineProperty.f(target, name, descriptor);
      };
    }
  });

  // node_modules/core-js-pure/internals/well-known-symbol-wrapped.js
  var require_well_known_symbol_wrapped = __commonJS({
    "node_modules/core-js-pure/internals/well-known-symbol-wrapped.js"(exports) {
      "use strict";
      var wellKnownSymbol = require_well_known_symbol();
      exports.f = wellKnownSymbol;
    }
  });

  // node_modules/core-js-pure/internals/well-known-symbol-define.js
  var require_well_known_symbol_define = __commonJS({
    "node_modules/core-js-pure/internals/well-known-symbol-define.js"(exports, module) {
      "use strict";
      var path = require_path();
      var hasOwn = require_has_own_property();
      var wrappedWellKnownSymbolModule = require_well_known_symbol_wrapped();
      var defineProperty = require_object_define_property().f;
      module.exports = function(NAME) {
        var Symbol2 = path.Symbol || (path.Symbol = {});
        if (!hasOwn(Symbol2, NAME)) defineProperty(Symbol2, NAME, {
          value: wrappedWellKnownSymbolModule.f(NAME)
        });
      };
    }
  });

  // node_modules/core-js-pure/internals/symbol-define-to-primitive.js
  var require_symbol_define_to_primitive = __commonJS({
    "node_modules/core-js-pure/internals/symbol-define-to-primitive.js"(exports, module) {
      "use strict";
      var call = require_function_call();
      var getBuiltIn = require_get_built_in();
      var wellKnownSymbol = require_well_known_symbol();
      var defineBuiltIn = require_define_built_in();
      module.exports = function() {
        var Symbol2 = getBuiltIn("Symbol");
        var SymbolPrototype = Symbol2 && Symbol2.prototype;
        var valueOf = SymbolPrototype && SymbolPrototype.valueOf;
        var TO_PRIMITIVE = wellKnownSymbol("toPrimitive");
        if (SymbolPrototype && !SymbolPrototype[TO_PRIMITIVE]) {
          defineBuiltIn(SymbolPrototype, TO_PRIMITIVE, function(hint) {
            return call(valueOf, this);
          }, { arity: 1 });
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/array-iteration.js
  var require_array_iteration = __commonJS({
    "node_modules/core-js-pure/internals/array-iteration.js"(exports, module) {
      "use strict";
      var bind = require_function_bind_context();
      var IndexedObject = require_indexed_object();
      var toObject = require_to_object();
      var lengthOfArrayLike = require_length_of_array_like();
      var arraySpeciesCreate = require_array_species_create();
      var createProperty = require_create_property();
      var createMethod = function(TYPE) {
        var IS_MAP = TYPE === 1;
        var IS_FILTER = TYPE === 2;
        var IS_SOME = TYPE === 3;
        var IS_EVERY = TYPE === 4;
        var IS_FIND_INDEX = TYPE === 6;
        var IS_FILTER_REJECT = TYPE === 7;
        var NO_HOLES = TYPE === 5 || IS_FIND_INDEX;
        return function($this, callbackfn, that) {
          var O = toObject($this);
          var self2 = IndexedObject(O);
          var length = lengthOfArrayLike(self2);
          var boundFunction = bind(callbackfn, that);
          var index = 0;
          var resIndex = 0;
          var target = IS_MAP ? arraySpeciesCreate($this, length) : IS_FILTER || IS_FILTER_REJECT ? arraySpeciesCreate($this, 0) : void 0;
          var value, result;
          for (; length > index; index++) if (NO_HOLES || index in self2) {
            value = self2[index];
            result = boundFunction(value, index, O);
            if (TYPE) {
              if (IS_MAP) createProperty(target, index, result);
              else if (result) switch (TYPE) {
                case 3:
                  return true;
                // some
                case 5:
                  return value;
                // find
                case 6:
                  return index;
                // findIndex
                case 2:
                  createProperty(target, resIndex++, value);
              }
              else switch (TYPE) {
                case 4:
                  return false;
                // every
                case 7:
                  createProperty(target, resIndex++, value);
              }
            }
          }
          return IS_FIND_INDEX ? -1 : IS_SOME || IS_EVERY ? IS_EVERY : target;
        };
      };
      module.exports = {
        // `Array.prototype.forEach` method
        // https://tc39.es/ecma262/#sec-array.prototype.foreach
        forEach: createMethod(0),
        // `Array.prototype.map` method
        // https://tc39.es/ecma262/#sec-array.prototype.map
        map: createMethod(1),
        // `Array.prototype.filter` method
        // https://tc39.es/ecma262/#sec-array.prototype.filter
        filter: createMethod(2),
        // `Array.prototype.some` method
        // https://tc39.es/ecma262/#sec-array.prototype.some
        some: createMethod(3),
        // `Array.prototype.every` method
        // https://tc39.es/ecma262/#sec-array.prototype.every
        every: createMethod(4),
        // `Array.prototype.find` method
        // https://tc39.es/ecma262/#sec-array.prototype.find
        find: createMethod(5),
        // `Array.prototype.findIndex` method
        // https://tc39.es/ecma262/#sec-array.prototype.findIndex
        findIndex: createMethod(6),
        // `Array.prototype.filterReject` method
        // https://github.com/tc39/proposal-array-filtering
        filterReject: createMethod(7)
      };
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.constructor.js
  var require_es_symbol_constructor = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.constructor.js"() {
      "use strict";
      var $ = require_export();
      var globalThis2 = require_global_this();
      var call = require_function_call();
      var uncurryThis = require_function_uncurry_this();
      var IS_PURE = require_is_pure();
      var DESCRIPTORS = require_descriptors();
      var NATIVE_SYMBOL = require_symbol_constructor_detection();
      var fails = require_fails();
      var hasOwn = require_has_own_property();
      var isPrototypeOf = require_object_is_prototype_of();
      var anObject = require_an_object();
      var toIndexedObject = require_to_indexed_object();
      var toPropertyKey2 = require_to_property_key();
      var $toString = require_to_string();
      var createPropertyDescriptor = require_create_property_descriptor();
      var nativeObjectCreate = require_object_create();
      var objectKeys = require_object_keys();
      var getOwnPropertyNamesModule = require_object_get_own_property_names();
      var getOwnPropertyNamesExternal = require_object_get_own_property_names_external();
      var getOwnPropertySymbolsModule = require_object_get_own_property_symbols();
      var getOwnPropertyDescriptorModule = require_object_get_own_property_descriptor();
      var definePropertyModule = require_object_define_property();
      var definePropertiesModule = require_object_define_properties();
      var propertyIsEnumerableModule = require_object_property_is_enumerable();
      var defineBuiltIn = require_define_built_in();
      var defineBuiltInAccessor = require_define_built_in_accessor();
      var shared = require_shared();
      var sharedKey = require_shared_key();
      var hiddenKeys = require_hidden_keys();
      var uid = require_uid();
      var wellKnownSymbol = require_well_known_symbol();
      var wrappedWellKnownSymbolModule = require_well_known_symbol_wrapped();
      var defineWellKnownSymbol = require_well_known_symbol_define();
      var defineSymbolToPrimitive = require_symbol_define_to_primitive();
      var setToStringTag = require_set_to_string_tag();
      var InternalStateModule = require_internal_state();
      var $forEach = require_array_iteration().forEach;
      var HIDDEN = sharedKey("hidden");
      var SYMBOL = "Symbol";
      var PROTOTYPE = "prototype";
      var setInternalState = InternalStateModule.set;
      var getInternalState = InternalStateModule.getterFor(SYMBOL);
      var ObjectPrototype = Object[PROTOTYPE];
      var $Symbol = globalThis2.Symbol;
      var SymbolPrototype = $Symbol && $Symbol[PROTOTYPE];
      var RangeError2 = globalThis2.RangeError;
      var TypeError2 = globalThis2.TypeError;
      var QObject = globalThis2.QObject;
      var nativeGetOwnPropertyDescriptor = getOwnPropertyDescriptorModule.f;
      var nativeDefineProperty = definePropertyModule.f;
      var nativeGetOwnPropertyNames = getOwnPropertyNamesExternal.f;
      var nativePropertyIsEnumerable = propertyIsEnumerableModule.f;
      var push = uncurryThis([].push);
      var AllSymbols = shared("symbols");
      var ObjectPrototypeSymbols = shared("op-symbols");
      var WellKnownSymbolsStore = shared("wks");
      var USE_SETTER = !QObject || !QObject[PROTOTYPE] || !QObject[PROTOTYPE].findChild;
      var fallbackDefineProperty = function(O, P, Attributes) {
        var ObjectPrototypeDescriptor = nativeGetOwnPropertyDescriptor(ObjectPrototype, P);
        if (ObjectPrototypeDescriptor) delete ObjectPrototype[P];
        nativeDefineProperty(O, P, Attributes);
        if (ObjectPrototypeDescriptor && O !== ObjectPrototype) {
          nativeDefineProperty(ObjectPrototype, P, ObjectPrototypeDescriptor);
        }
        return O;
      };
      var setSymbolDescriptor = DESCRIPTORS && fails(function() {
        return nativeObjectCreate(nativeDefineProperty({}, "a", {
          get: function() {
            return nativeDefineProperty(this, "a", { value: 7 }).a;
          }
        })).a !== 7;
      }) ? fallbackDefineProperty : nativeDefineProperty;
      var wrap = function(tag, description) {
        var symbol = AllSymbols[tag] = nativeObjectCreate(SymbolPrototype);
        setInternalState(symbol, {
          type: SYMBOL,
          tag,
          description
        });
        if (!DESCRIPTORS) symbol.description = description;
        return symbol;
      };
      var $defineProperty = function defineProperty(O, P, Attributes) {
        if (O === ObjectPrototype) $defineProperty(ObjectPrototypeSymbols, P, Attributes);
        anObject(O);
        var key = toPropertyKey2(P);
        anObject(Attributes);
        if (hasOwn(AllSymbols, key)) {
          if (!("enumerable" in Attributes) ? !hasOwn(O, key) || hasOwn(O, HIDDEN) && O[HIDDEN][key] : !Attributes.enumerable) {
            if (!hasOwn(O, HIDDEN)) nativeDefineProperty(O, HIDDEN, createPropertyDescriptor(1, nativeObjectCreate(null)));
            O[HIDDEN][key] = true;
          } else {
            if (hasOwn(O, HIDDEN) && O[HIDDEN][key]) O[HIDDEN][key] = false;
            Attributes = nativeObjectCreate(Attributes, { enumerable: createPropertyDescriptor(0, false) });
          }
          return setSymbolDescriptor(O, key, Attributes);
        }
        return nativeDefineProperty(O, key, Attributes);
      };
      var $defineProperties = function defineProperties(O, Properties) {
        anObject(O);
        var properties = toIndexedObject(Properties);
        var keys = objectKeys(properties).concat($getOwnPropertySymbols(properties));
        $forEach(keys, function(key) {
          if (!DESCRIPTORS || call($propertyIsEnumerable, properties, key)) $defineProperty(O, key, properties[key]);
        });
        return O;
      };
      var $create = function create(O, Properties) {
        return Properties === void 0 ? nativeObjectCreate(O) : $defineProperties(nativeObjectCreate(O), Properties);
      };
      var $propertyIsEnumerable = function propertyIsEnumerable(V) {
        var P = toPropertyKey2(V);
        var enumerable = call(nativePropertyIsEnumerable, this, P);
        if (this === ObjectPrototype && hasOwn(AllSymbols, P) && !hasOwn(ObjectPrototypeSymbols, P)) return false;
        return enumerable || !hasOwn(this, P) || !hasOwn(AllSymbols, P) || hasOwn(this, HIDDEN) && this[HIDDEN][P] ? enumerable : true;
      };
      var $getOwnPropertyDescriptor = function getOwnPropertyDescriptor(O, P) {
        var it = toIndexedObject(O);
        var key = toPropertyKey2(P);
        if (it === ObjectPrototype && hasOwn(AllSymbols, key) && !hasOwn(ObjectPrototypeSymbols, key)) return;
        var descriptor = nativeGetOwnPropertyDescriptor(it, key);
        if (descriptor && hasOwn(AllSymbols, key) && !(hasOwn(it, HIDDEN) && it[HIDDEN][key])) {
          descriptor.enumerable = true;
        }
        return descriptor;
      };
      var $getOwnPropertyNames = function getOwnPropertyNames(O) {
        var names = nativeGetOwnPropertyNames(toIndexedObject(O));
        var result = [];
        $forEach(names, function(key) {
          if (!hasOwn(AllSymbols, key) && !hasOwn(hiddenKeys, key)) push(result, key);
        });
        return result;
      };
      var $getOwnPropertySymbols = function(O) {
        var IS_OBJECT_PROTOTYPE = O === ObjectPrototype;
        var names = nativeGetOwnPropertyNames(IS_OBJECT_PROTOTYPE ? ObjectPrototypeSymbols : toIndexedObject(O));
        var result = [];
        $forEach(names, function(key) {
          if (hasOwn(AllSymbols, key) && (!IS_OBJECT_PROTOTYPE || hasOwn(ObjectPrototype, key))) {
            push(result, AllSymbols[key]);
          }
        });
        return result;
      };
      if (!NATIVE_SYMBOL) {
        $Symbol = function Symbol2() {
          if (isPrototypeOf(SymbolPrototype, this)) throw new TypeError2("Symbol is not a constructor");
          var description = !arguments.length || arguments[0] === void 0 ? void 0 : $toString(arguments[0]);
          var tag = uid(description);
          var setter = function(value) {
            var $this = this === void 0 ? globalThis2 : this;
            if ($this === ObjectPrototype) call(setter, ObjectPrototypeSymbols, value);
            if (hasOwn($this, HIDDEN) && hasOwn($this[HIDDEN], tag)) $this[HIDDEN][tag] = false;
            var descriptor = createPropertyDescriptor(1, value);
            try {
              setSymbolDescriptor($this, tag, descriptor);
            } catch (error) {
              if (!(error instanceof RangeError2)) throw error;
              fallbackDefineProperty($this, tag, descriptor);
            }
          };
          if (DESCRIPTORS && USE_SETTER) setSymbolDescriptor(ObjectPrototype, tag, { configurable: true, set: setter });
          return wrap(tag, description);
        };
        SymbolPrototype = $Symbol[PROTOTYPE];
        defineBuiltIn(SymbolPrototype, "toString", function toString() {
          return getInternalState(this).tag;
        });
        defineBuiltIn($Symbol, "withoutSetter", function(description) {
          return wrap(uid(description), description);
        });
        propertyIsEnumerableModule.f = $propertyIsEnumerable;
        definePropertyModule.f = $defineProperty;
        definePropertiesModule.f = $defineProperties;
        getOwnPropertyDescriptorModule.f = $getOwnPropertyDescriptor;
        getOwnPropertyNamesModule.f = getOwnPropertyNamesExternal.f = $getOwnPropertyNames;
        getOwnPropertySymbolsModule.f = $getOwnPropertySymbols;
        wrappedWellKnownSymbolModule.f = function(name) {
          return wrap(wellKnownSymbol(name), name);
        };
        if (DESCRIPTORS) {
          defineBuiltInAccessor(SymbolPrototype, "description", {
            configurable: true,
            get: function description() {
              return getInternalState(this).description;
            }
          });
          if (!IS_PURE) {
            defineBuiltIn(ObjectPrototype, "propertyIsEnumerable", $propertyIsEnumerable, { unsafe: true });
          }
        }
      }
      $({ global: true, constructor: true, wrap: true, forced: !NATIVE_SYMBOL, sham: !NATIVE_SYMBOL }, {
        Symbol: $Symbol
      });
      $forEach(objectKeys(WellKnownSymbolsStore), function(name) {
        defineWellKnownSymbol(name);
      });
      $({ target: SYMBOL, stat: true, forced: !NATIVE_SYMBOL }, {
        useSetter: function() {
          USE_SETTER = true;
        },
        useSimple: function() {
          USE_SETTER = false;
        }
      });
      $({ target: "Object", stat: true, forced: !NATIVE_SYMBOL, sham: !DESCRIPTORS }, {
        // `Object.create` method
        // https://tc39.es/ecma262/#sec-object.create
        create: $create,
        // `Object.defineProperty` method
        // https://tc39.es/ecma262/#sec-object.defineproperty
        defineProperty: $defineProperty,
        // `Object.defineProperties` method
        // https://tc39.es/ecma262/#sec-object.defineproperties
        defineProperties: $defineProperties,
        // `Object.getOwnPropertyDescriptor` method
        // https://tc39.es/ecma262/#sec-object.getownpropertydescriptors
        getOwnPropertyDescriptor: $getOwnPropertyDescriptor
      });
      $({ target: "Object", stat: true, forced: !NATIVE_SYMBOL }, {
        // `Object.getOwnPropertyNames` method
        // https://tc39.es/ecma262/#sec-object.getownpropertynames
        getOwnPropertyNames: $getOwnPropertyNames
      });
      defineSymbolToPrimitive();
      setToStringTag($Symbol, SYMBOL);
      hiddenKeys[HIDDEN] = true;
    }
  });

  // node_modules/core-js-pure/internals/symbol-registry-detection.js
  var require_symbol_registry_detection = __commonJS({
    "node_modules/core-js-pure/internals/symbol-registry-detection.js"(exports, module) {
      "use strict";
      var NATIVE_SYMBOL = require_symbol_constructor_detection();
      module.exports = NATIVE_SYMBOL && !!Symbol["for"] && !!Symbol.keyFor;
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.for.js
  var require_es_symbol_for = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.for.js"() {
      "use strict";
      var $ = require_export();
      var getBuiltIn = require_get_built_in();
      var hasOwn = require_has_own_property();
      var toString = require_to_string();
      var shared = require_shared();
      var NATIVE_SYMBOL_REGISTRY = require_symbol_registry_detection();
      var StringToSymbolRegistry = shared("string-to-symbol-registry");
      var SymbolToStringRegistry = shared("symbol-to-string-registry");
      $({ target: "Symbol", stat: true, forced: !NATIVE_SYMBOL_REGISTRY }, {
        "for": function(key) {
          var string = toString(key);
          if (hasOwn(StringToSymbolRegistry, string)) return StringToSymbolRegistry[string];
          var symbol = getBuiltIn("Symbol")(string);
          StringToSymbolRegistry[string] = symbol;
          SymbolToStringRegistry[symbol] = string;
          return symbol;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.key-for.js
  var require_es_symbol_key_for = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.key-for.js"() {
      "use strict";
      var $ = require_export();
      var hasOwn = require_has_own_property();
      var isSymbol = require_is_symbol();
      var tryToString = require_try_to_string();
      var shared = require_shared();
      var NATIVE_SYMBOL_REGISTRY = require_symbol_registry_detection();
      var SymbolToStringRegistry = shared("symbol-to-string-registry");
      $({ target: "Symbol", stat: true, forced: !NATIVE_SYMBOL_REGISTRY }, {
        keyFor: function keyFor(sym) {
          if (!isSymbol(sym)) throw new TypeError(tryToString(sym) + " is not a symbol");
          if (hasOwn(SymbolToStringRegistry, sym)) return SymbolToStringRegistry[sym];
        }
      });
    }
  });

  // node_modules/core-js-pure/internals/is-raw-json.js
  var require_is_raw_json = __commonJS({
    "node_modules/core-js-pure/internals/is-raw-json.js"(exports, module) {
      "use strict";
      var isObject = require_is_object();
      var getInternalState = require_internal_state().get;
      module.exports = function isRawJSON(O) {
        if (!isObject(O)) return false;
        var state = getInternalState(O);
        return !!state && state.type === "RawJSON";
      };
    }
  });

  // node_modules/core-js-pure/internals/parse-json-string.js
  var require_parse_json_string = __commonJS({
    "node_modules/core-js-pure/internals/parse-json-string.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var hasOwn = require_has_own_property();
      var $SyntaxError = SyntaxError;
      var $parseInt = parseInt;
      var fromCharCode = String.fromCharCode;
      var at = uncurryThis("".charAt);
      var slice = uncurryThis("".slice);
      var exec = uncurryThis(/./.exec);
      var codePoints = {
        '\\"': '"',
        "\\\\": "\\",
        "\\/": "/",
        "\\b": "\b",
        "\\f": "\f",
        "\\n": "\n",
        "\\r": "\r",
        "\\t": "	"
      };
      var IS_4_HEX_DIGITS = /^[\da-f]{4}$/i;
      var IS_C0_CONTROL_CODE = /^[\u0000-\u001F]$/;
      module.exports = function(source, i) {
        var unterminated = true;
        var value = "";
        while (i < source.length) {
          var chr = at(source, i);
          if (chr === "\\") {
            var twoChars = slice(source, i, i + 2);
            if (hasOwn(codePoints, twoChars)) {
              value += codePoints[twoChars];
              i += 2;
            } else if (twoChars === "\\u") {
              i += 2;
              var fourHexDigits = slice(source, i, i + 4);
              if (!exec(IS_4_HEX_DIGITS, fourHexDigits)) throw new $SyntaxError("Bad Unicode escape at: " + i);
              value += fromCharCode($parseInt(fourHexDigits, 16));
              i += 4;
            } else throw new $SyntaxError('Unknown escape sequence: "' + twoChars + '"');
          } else if (chr === '"') {
            unterminated = false;
            i++;
            break;
          } else {
            if (exec(IS_C0_CONTROL_CODE, chr)) throw new $SyntaxError("Bad control character in string literal at: " + i);
            value += chr;
            i++;
          }
        }
        if (unterminated) throw new $SyntaxError("Unterminated string at: " + i);
        return { value, end: i };
      };
    }
  });

  // node_modules/core-js-pure/internals/native-raw-json.js
  var require_native_raw_json = __commonJS({
    "node_modules/core-js-pure/internals/native-raw-json.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      module.exports = !fails(function() {
        var unsafeInt = "9007199254740993";
        var raw = JSON.rawJSON(unsafeInt);
        return !JSON.isRawJSON(raw) || JSON.stringify(raw) !== unsafeInt;
      });
    }
  });

  // node_modules/core-js-pure/modules/es.json.stringify.js
  var require_es_json_stringify = __commonJS({
    "node_modules/core-js-pure/modules/es.json.stringify.js"() {
      "use strict";
      var $ = require_export();
      var getBuiltIn = require_get_built_in();
      var apply = require_function_apply();
      var call = require_function_call();
      var uncurryThis = require_function_uncurry_this();
      var fails = require_fails();
      var isArray = require_is_array();
      var isCallable = require_is_callable();
      var isRawJSON = require_is_raw_json();
      var isSymbol = require_is_symbol();
      var classof = require_classof_raw();
      var toString = require_to_string();
      var arraySlice = require_array_slice();
      var parseJSONString = require_parse_json_string();
      var uid = require_uid();
      var NATIVE_SYMBOL = require_symbol_constructor_detection();
      var NATIVE_RAW_JSON = require_native_raw_json();
      var $String = String;
      var $stringify = getBuiltIn("JSON", "stringify");
      var exec = uncurryThis(/./.exec);
      var charAt = uncurryThis("".charAt);
      var charCodeAt = uncurryThis("".charCodeAt);
      var replace = uncurryThis("".replace);
      var slice = uncurryThis("".slice);
      var push = uncurryThis([].push);
      var numberToString = uncurryThis(1.1.toString);
      var surrogates = /[\uD800-\uDFFF]/g;
      var leadingSurrogates = /^[\uD800-\uDBFF]$/;
      var trailingSurrogates = /^[\uDC00-\uDFFF]$/;
      var MARK = uid();
      var MARK_LENGTH = MARK.length;
      var WRONG_SYMBOLS_CONVERSION = !NATIVE_SYMBOL || fails(function() {
        var symbol = getBuiltIn("Symbol")("stringify detection");
        return $stringify([symbol]) !== "[null]" || $stringify({ a: symbol }) !== "{}" || $stringify(Object(symbol)) !== "{}";
      });
      var ILL_FORMED_UNICODE = fails(function() {
        return $stringify("\uDF06\uD834") !== '"\\udf06\\ud834"' || $stringify("\uDEAD") !== '"\\udead"';
      });
      var stringifyWithProperSymbolsConversion = WRONG_SYMBOLS_CONVERSION ? function(it, replacer) {
        var args = arraySlice(arguments);
        var $replacer = getReplacerFunction(replacer);
        if (!isCallable($replacer) && (it === void 0 || isSymbol(it))) return;
        args[1] = function(key, value) {
          if (isCallable($replacer)) value = call($replacer, this, $String(key), value);
          if (!isSymbol(value)) return value;
        };
        return apply($stringify, null, args);
      } : $stringify;
      var fixIllFormedJSON = function(match, offset, string) {
        var prev = charAt(string, offset - 1);
        var next = charAt(string, offset + 1);
        if (exec(leadingSurrogates, match) && !exec(trailingSurrogates, next) || exec(trailingSurrogates, match) && !exec(leadingSurrogates, prev)) {
          return "\\u" + numberToString(charCodeAt(match, 0), 16);
        }
        return match;
      };
      var getReplacerFunction = function(replacer) {
        if (isCallable(replacer)) return replacer;
        if (!isArray(replacer)) return;
        var rawLength = replacer.length;
        var keys = [];
        for (var i = 0; i < rawLength; i++) {
          var element = replacer[i];
          if (typeof element == "string") push(keys, element);
          else if (typeof element == "number" || classof(element) === "Number" || classof(element) === "String") push(keys, toString(element));
        }
        var keysLength = keys.length;
        var root = true;
        return function(key, value) {
          if (root) {
            root = false;
            return value;
          }
          if (isArray(this)) return value;
          for (var j = 0; j < keysLength; j++) if (keys[j] === key) return value;
        };
      };
      if ($stringify) $({ target: "JSON", stat: true, arity: 3, forced: WRONG_SYMBOLS_CONVERSION || ILL_FORMED_UNICODE || !NATIVE_RAW_JSON }, {
        stringify: function stringify(text, replacer, space) {
          var replacerFunction = getReplacerFunction(replacer);
          var rawStrings = [];
          var json = stringifyWithProperSymbolsConversion(text, function(key, value) {
            var v = isCallable(replacerFunction) ? call(replacerFunction, this, $String(key), value) : value;
            return !NATIVE_RAW_JSON && isRawJSON(v) ? MARK + (push(rawStrings, v.rawJSON) - 1) : v;
          }, space);
          if (typeof json != "string") return json;
          if (ILL_FORMED_UNICODE) json = replace(json, surrogates, fixIllFormedJSON);
          if (NATIVE_RAW_JSON) return json;
          var result = "";
          var length = json.length;
          for (var i = 0; i < length; i++) {
            var chr = charAt(json, i);
            if (chr === '"') {
              var end = parseJSONString(json, ++i).end - 1;
              var string = slice(json, i, end);
              result += slice(string, 0, MARK_LENGTH) === MARK ? rawStrings[slice(string, MARK_LENGTH)] : '"' + string + '"';
              i = end;
            } else result += chr;
          }
          return result;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.object.get-own-property-symbols.js
  var require_es_object_get_own_property_symbols = __commonJS({
    "node_modules/core-js-pure/modules/es.object.get-own-property-symbols.js"() {
      "use strict";
      var $ = require_export();
      var NATIVE_SYMBOL = require_symbol_constructor_detection();
      var fails = require_fails();
      var getOwnPropertySymbolsModule = require_object_get_own_property_symbols();
      var toObject = require_to_object();
      var FORCED = !NATIVE_SYMBOL || fails(function() {
        getOwnPropertySymbolsModule.f(1);
      });
      $({ target: "Object", stat: true, forced: FORCED }, {
        getOwnPropertySymbols: function getOwnPropertySymbols(it) {
          var $getOwnPropertySymbols = getOwnPropertySymbolsModule.f;
          return $getOwnPropertySymbols ? $getOwnPropertySymbols(toObject(it)) : [];
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.js
  var require_es_symbol = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.js"() {
      "use strict";
      require_es_symbol_constructor();
      require_es_symbol_for();
      require_es_symbol_key_for();
      require_es_json_stringify();
      require_es_object_get_own_property_symbols();
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.async-dispose.js
  var require_es_symbol_async_dispose = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.async-dispose.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("asyncDispose");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.async-iterator.js
  var require_es_symbol_async_iterator = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.async-iterator.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("asyncIterator");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.description.js
  var require_es_symbol_description = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.description.js"() {
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.dispose.js
  var require_es_symbol_dispose = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.dispose.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("dispose");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.has-instance.js
  var require_es_symbol_has_instance = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.has-instance.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("hasInstance");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.is-concat-spreadable.js
  var require_es_symbol_is_concat_spreadable = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.is-concat-spreadable.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("isConcatSpreadable");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.iterator.js
  var require_es_symbol_iterator = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.iterator.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("iterator");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.match.js
  var require_es_symbol_match = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.match.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("match");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.match-all.js
  var require_es_symbol_match_all = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.match-all.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("matchAll");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.replace.js
  var require_es_symbol_replace = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.replace.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("replace");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.search.js
  var require_es_symbol_search = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.search.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("search");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.species.js
  var require_es_symbol_species = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.species.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("species");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.split.js
  var require_es_symbol_split = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.split.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("split");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.to-primitive.js
  var require_es_symbol_to_primitive = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.to-primitive.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      var defineSymbolToPrimitive = require_symbol_define_to_primitive();
      defineWellKnownSymbol("toPrimitive");
      defineSymbolToPrimitive();
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.to-string-tag.js
  var require_es_symbol_to_string_tag = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.to-string-tag.js"() {
      "use strict";
      var getBuiltIn = require_get_built_in();
      var defineWellKnownSymbol = require_well_known_symbol_define();
      var setToStringTag = require_set_to_string_tag();
      defineWellKnownSymbol("toStringTag");
      setToStringTag(getBuiltIn("Symbol"), "Symbol");
    }
  });

  // node_modules/core-js-pure/modules/es.symbol.unscopables.js
  var require_es_symbol_unscopables = __commonJS({
    "node_modules/core-js-pure/modules/es.symbol.unscopables.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("unscopables");
    }
  });

  // node_modules/core-js-pure/modules/es.json.to-string-tag.js
  var require_es_json_to_string_tag = __commonJS({
    "node_modules/core-js-pure/modules/es.json.to-string-tag.js"() {
      "use strict";
      var globalThis2 = require_global_this();
      var setToStringTag = require_set_to_string_tag();
      setToStringTag(globalThis2.JSON, "JSON", true);
    }
  });

  // node_modules/core-js-pure/modules/es.math.to-string-tag.js
  var require_es_math_to_string_tag = __commonJS({
    "node_modules/core-js-pure/modules/es.math.to-string-tag.js"() {
    }
  });

  // node_modules/core-js-pure/modules/es.reflect.to-string-tag.js
  var require_es_reflect_to_string_tag = __commonJS({
    "node_modules/core-js-pure/modules/es.reflect.to-string-tag.js"() {
    }
  });

  // node_modules/core-js-pure/es/symbol/index.js
  var require_symbol = __commonJS({
    "node_modules/core-js-pure/es/symbol/index.js"(exports, module) {
      "use strict";
      require_es_array_concat();
      require_es_object_to_string();
      require_es_symbol();
      require_es_symbol_async_dispose();
      require_es_symbol_async_iterator();
      require_es_symbol_description();
      require_es_symbol_dispose();
      require_es_symbol_has_instance();
      require_es_symbol_is_concat_spreadable();
      require_es_symbol_iterator();
      require_es_symbol_match();
      require_es_symbol_match_all();
      require_es_symbol_replace();
      require_es_symbol_search();
      require_es_symbol_species();
      require_es_symbol_split();
      require_es_symbol_to_primitive();
      require_es_symbol_to_string_tag();
      require_es_symbol_unscopables();
      require_es_json_to_string_tag();
      require_es_math_to_string_tag();
      require_es_reflect_to_string_tag();
      var path = require_path();
      module.exports = path.Symbol;
    }
  });

  // node_modules/core-js-pure/internals/add-to-unscopables.js
  var require_add_to_unscopables = __commonJS({
    "node_modules/core-js-pure/internals/add-to-unscopables.js"(exports, module) {
      "use strict";
      module.exports = function() {
      };
    }
  });

  // node_modules/core-js-pure/modules/es.array.iterator.js
  var require_es_array_iterator = __commonJS({
    "node_modules/core-js-pure/modules/es.array.iterator.js"(exports, module) {
      "use strict";
      var toIndexedObject = require_to_indexed_object();
      var addToUnscopables = require_add_to_unscopables();
      var Iterators = require_iterators();
      var InternalStateModule = require_internal_state();
      var defineProperty = require_object_define_property().f;
      var defineIterator = require_iterator_define();
      var createIterResultObject = require_create_iter_result_object();
      var IS_PURE = require_is_pure();
      var DESCRIPTORS = require_descriptors();
      var ARRAY_ITERATOR = "Array Iterator";
      var setInternalState = InternalStateModule.set;
      var getInternalState = InternalStateModule.getterFor(ARRAY_ITERATOR);
      module.exports = defineIterator(Array, "Array", function(iterated, kind) {
        setInternalState(this, {
          type: ARRAY_ITERATOR,
          target: toIndexedObject(iterated),
          // target
          index: 0,
          // next index
          kind
          // kind
        });
      }, function() {
        var state = getInternalState(this);
        var target = state.target;
        var index = state.index++;
        if (!target || index >= target.length) {
          state.target = null;
          return createIterResultObject(void 0, true);
        }
        switch (state.kind) {
          case "keys":
            return createIterResultObject(index, false);
          case "values":
            return createIterResultObject(target[index], false);
        }
        return createIterResultObject([index, target[index]], false);
      }, "values");
      var values = Iterators.Arguments = Iterators.Array;
      addToUnscopables("keys");
      addToUnscopables("values");
      addToUnscopables("entries");
      if (!IS_PURE && DESCRIPTORS && values.name !== "values") try {
        defineProperty(values, "name", { value: "values" });
      } catch (error) {
      }
    }
  });

  // node_modules/core-js-pure/internals/dom-iterables.js
  var require_dom_iterables = __commonJS({
    "node_modules/core-js-pure/internals/dom-iterables.js"(exports, module) {
      "use strict";
      module.exports = {
        CSSRuleList: 0,
        CSSStyleDeclaration: 0,
        CSSValueList: 0,
        ClientRectList: 0,
        DOMRectList: 0,
        DOMStringList: 0,
        DOMTokenList: 1,
        DataTransferItemList: 0,
        FileList: 0,
        HTMLAllCollection: 0,
        HTMLCollection: 0,
        HTMLFormElement: 0,
        HTMLSelectElement: 0,
        MediaList: 0,
        MimeTypeArray: 0,
        NamedNodeMap: 0,
        NodeList: 1,
        PaintRequestList: 0,
        Plugin: 0,
        PluginArray: 0,
        SVGLengthList: 0,
        SVGNumberList: 0,
        SVGPathSegList: 0,
        SVGPointList: 0,
        SVGStringList: 0,
        SVGTransformList: 0,
        SourceBufferList: 0,
        StyleSheetList: 0,
        TextTrackCueList: 0,
        TextTrackList: 0,
        TouchList: 0
      };
    }
  });

  // node_modules/core-js-pure/modules/web.dom-collections.iterator.js
  var require_web_dom_collections_iterator = __commonJS({
    "node_modules/core-js-pure/modules/web.dom-collections.iterator.js"() {
      "use strict";
      require_es_array_iterator();
      var DOMIterables = require_dom_iterables();
      var globalThis2 = require_global_this();
      var setToStringTag = require_set_to_string_tag();
      var Iterators = require_iterators();
      for (COLLECTION_NAME in DOMIterables) {
        setToStringTag(globalThis2[COLLECTION_NAME], COLLECTION_NAME);
        Iterators[COLLECTION_NAME] = Iterators.Array;
      }
      var COLLECTION_NAME;
    }
  });

  // node_modules/core-js-pure/stable/symbol/index.js
  var require_symbol2 = __commonJS({
    "node_modules/core-js-pure/stable/symbol/index.js"(exports, module) {
      "use strict";
      var parent = require_symbol();
      require_web_dom_collections_iterator();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/modules/esnext.function.metadata.js
  var require_esnext_function_metadata = __commonJS({
    "node_modules/core-js-pure/modules/esnext.function.metadata.js"() {
      "use strict";
      var wellKnownSymbol = require_well_known_symbol();
      var defineProperty = require_object_define_property().f;
      var METADATA = wellKnownSymbol("metadata");
      var FunctionPrototype = Function.prototype;
      if (FunctionPrototype[METADATA] === void 0) {
        defineProperty(FunctionPrototype, METADATA, {
          value: null
        });
      }
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.async-dispose.js
  var require_esnext_symbol_async_dispose = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.async-dispose.js"() {
      "use strict";
      require_es_symbol_async_dispose();
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.dispose.js
  var require_esnext_symbol_dispose = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.dispose.js"() {
      "use strict";
      require_es_symbol_dispose();
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.metadata.js
  var require_esnext_symbol_metadata = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.metadata.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("metadata");
    }
  });

  // node_modules/core-js-pure/actual/symbol/index.js
  var require_symbol3 = __commonJS({
    "node_modules/core-js-pure/actual/symbol/index.js"(exports, module) {
      "use strict";
      var parent = require_symbol2();
      require_esnext_function_metadata();
      require_esnext_symbol_async_dispose();
      require_esnext_symbol_dispose();
      require_esnext_symbol_metadata();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/internals/symbol-is-registered.js
  var require_symbol_is_registered = __commonJS({
    "node_modules/core-js-pure/internals/symbol-is-registered.js"(exports, module) {
      "use strict";
      var getBuiltIn = require_get_built_in();
      var uncurryThis = require_function_uncurry_this();
      var Symbol2 = getBuiltIn("Symbol");
      var keyFor = Symbol2.keyFor;
      var thisSymbolValue = uncurryThis(Symbol2.prototype.valueOf);
      module.exports = Symbol2.isRegisteredSymbol || function isRegisteredSymbol(value) {
        try {
          return keyFor(thisSymbolValue(value)) !== void 0;
        } catch (error) {
          return false;
        }
      };
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.is-registered-symbol.js
  var require_esnext_symbol_is_registered_symbol = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.is-registered-symbol.js"() {
      "use strict";
      var $ = require_export();
      var isRegisteredSymbol = require_symbol_is_registered();
      $({ target: "Symbol", stat: true }, {
        isRegisteredSymbol
      });
    }
  });

  // node_modules/core-js-pure/internals/symbol-is-well-known.js
  var require_symbol_is_well_known = __commonJS({
    "node_modules/core-js-pure/internals/symbol-is-well-known.js"(exports, module) {
      "use strict";
      var shared = require_shared();
      var getBuiltIn = require_get_built_in();
      var uncurryThis = require_function_uncurry_this();
      var isSymbol = require_is_symbol();
      var wellKnownSymbol = require_well_known_symbol();
      var Symbol2 = getBuiltIn("Symbol");
      var $isWellKnownSymbol = Symbol2.isWellKnownSymbol;
      var getOwnPropertyNames = getBuiltIn("Object", "getOwnPropertyNames");
      var thisSymbolValue = uncurryThis(Symbol2.prototype.valueOf);
      var WellKnownSymbolsStore = shared("wks");
      for (i = 0, symbolKeys = getOwnPropertyNames(Symbol2), symbolKeysLength = symbolKeys.length; i < symbolKeysLength; i++) {
        try {
          symbolKey = symbolKeys[i];
          if (isSymbol(Symbol2[symbolKey])) wellKnownSymbol(symbolKey);
        } catch (error) {
        }
      }
      var symbolKey;
      var i;
      var symbolKeys;
      var symbolKeysLength;
      module.exports = function isWellKnownSymbol(value) {
        if ($isWellKnownSymbol && $isWellKnownSymbol(value)) return true;
        try {
          var symbol = thisSymbolValue(value);
          for (var j = 0, keys = getOwnPropertyNames(WellKnownSymbolsStore), keysLength = keys.length; j < keysLength; j++) {
            if (WellKnownSymbolsStore[keys[j]] == symbol) return true;
          }
        } catch (error) {
        }
        return false;
      };
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.is-well-known-symbol.js
  var require_esnext_symbol_is_well_known_symbol = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.is-well-known-symbol.js"() {
      "use strict";
      var $ = require_export();
      var isWellKnownSymbol = require_symbol_is_well_known();
      $({ target: "Symbol", stat: true, forced: true }, {
        isWellKnownSymbol
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.custom-matcher.js
  var require_esnext_symbol_custom_matcher = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.custom-matcher.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("customMatcher");
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.observable.js
  var require_esnext_symbol_observable = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.observable.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("observable");
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.is-registered.js
  var require_esnext_symbol_is_registered = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.is-registered.js"() {
      "use strict";
      var $ = require_export();
      var isRegisteredSymbol = require_symbol_is_registered();
      $({ target: "Symbol", stat: true, name: "isRegisteredSymbol" }, {
        isRegistered: isRegisteredSymbol
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.is-well-known.js
  var require_esnext_symbol_is_well_known = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.is-well-known.js"() {
      "use strict";
      var $ = require_export();
      var isWellKnownSymbol = require_symbol_is_well_known();
      $({ target: "Symbol", stat: true, name: "isWellKnownSymbol", forced: true }, {
        isWellKnown: isWellKnownSymbol
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.matcher.js
  var require_esnext_symbol_matcher = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.matcher.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("matcher");
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.metadata-key.js
  var require_esnext_symbol_metadata_key = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.metadata-key.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("metadataKey");
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.pattern-match.js
  var require_esnext_symbol_pattern_match = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.pattern-match.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("patternMatch");
    }
  });

  // node_modules/core-js-pure/modules/esnext.symbol.replace-all.js
  var require_esnext_symbol_replace_all = __commonJS({
    "node_modules/core-js-pure/modules/esnext.symbol.replace-all.js"() {
      "use strict";
      var defineWellKnownSymbol = require_well_known_symbol_define();
      defineWellKnownSymbol("replaceAll");
    }
  });

  // node_modules/core-js-pure/full/symbol/index.js
  var require_symbol4 = __commonJS({
    "node_modules/core-js-pure/full/symbol/index.js"(exports, module) {
      "use strict";
      var parent = require_symbol3();
      require_esnext_symbol_is_registered_symbol();
      require_esnext_symbol_is_well_known_symbol();
      require_esnext_symbol_custom_matcher();
      require_esnext_symbol_observable();
      require_esnext_symbol_is_registered();
      require_esnext_symbol_is_well_known();
      require_esnext_symbol_matcher();
      require_esnext_symbol_metadata_key();
      require_esnext_symbol_pattern_match();
      require_esnext_symbol_replace_all();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/symbol/index.js
  var require_symbol5 = __commonJS({
    "node_modules/core-js-pure/features/symbol/index.js"(exports, module) {
      "use strict";
      module.exports = require_symbol4();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/symbol.js
  var require_symbol6 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/symbol.js"(exports, module) {
      module.exports = require_symbol5();
    }
  });

  // node_modules/core-js-pure/es/get-iterator-method.js
  var require_get_iterator_method2 = __commonJS({
    "node_modules/core-js-pure/es/get-iterator-method.js"(exports, module) {
      "use strict";
      require_es_array_iterator();
      require_es_string_iterator();
      var getIteratorMethod = require_get_iterator_method();
      module.exports = getIteratorMethod;
    }
  });

  // node_modules/core-js-pure/stable/get-iterator-method.js
  var require_get_iterator_method3 = __commonJS({
    "node_modules/core-js-pure/stable/get-iterator-method.js"(exports, module) {
      "use strict";
      var parent = require_get_iterator_method2();
      require_web_dom_collections_iterator();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/get-iterator-method.js
  var require_get_iterator_method4 = __commonJS({
    "node_modules/core-js-pure/actual/get-iterator-method.js"(exports, module) {
      "use strict";
      var parent = require_get_iterator_method3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/get-iterator-method.js
  var require_get_iterator_method5 = __commonJS({
    "node_modules/core-js-pure/full/get-iterator-method.js"(exports, module) {
      "use strict";
      var parent = require_get_iterator_method4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/get-iterator-method.js
  var require_get_iterator_method6 = __commonJS({
    "node_modules/core-js-pure/features/get-iterator-method.js"(exports, module) {
      "use strict";
      module.exports = require_get_iterator_method5();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/get-iterator-method.js
  var require_get_iterator_method7 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/get-iterator-method.js"(exports, module) {
      module.exports = require_get_iterator_method6();
    }
  });

  // node_modules/core-js-pure/es/get-iterator.js
  var require_get_iterator2 = __commonJS({
    "node_modules/core-js-pure/es/get-iterator.js"(exports, module) {
      "use strict";
      require_es_array_iterator();
      require_es_string_iterator();
      var getIterator = require_get_iterator();
      module.exports = getIterator;
    }
  });

  // node_modules/core-js-pure/stable/get-iterator.js
  var require_get_iterator3 = __commonJS({
    "node_modules/core-js-pure/stable/get-iterator.js"(exports, module) {
      "use strict";
      var parent = require_get_iterator2();
      require_web_dom_collections_iterator();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/get-iterator.js
  var require_get_iterator4 = __commonJS({
    "node_modules/core-js-pure/actual/get-iterator.js"(exports, module) {
      "use strict";
      var parent = require_get_iterator3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/get-iterator.js
  var require_get_iterator5 = __commonJS({
    "node_modules/core-js-pure/full/get-iterator.js"(exports, module) {
      "use strict";
      var parent = require_get_iterator4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/get-iterator.js
  var require_get_iterator6 = __commonJS({
    "node_modules/core-js-pure/features/get-iterator.js"(exports, module) {
      "use strict";
      module.exports = require_get_iterator5();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/get-iterator.js
  var require_get_iterator7 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/get-iterator.js"(exports, module) {
      module.exports = require_get_iterator6();
    }
  });

  // node_modules/core-js-pure/internals/own-keys.js
  var require_own_keys = __commonJS({
    "node_modules/core-js-pure/internals/own-keys.js"(exports, module) {
      "use strict";
      var getBuiltIn = require_get_built_in();
      var uncurryThis = require_function_uncurry_this();
      var getOwnPropertyNamesModule = require_object_get_own_property_names();
      var getOwnPropertySymbolsModule = require_object_get_own_property_symbols();
      var anObject = require_an_object();
      var concat = uncurryThis([].concat);
      module.exports = getBuiltIn("Reflect", "ownKeys") || function ownKeys(it) {
        var keys = getOwnPropertyNamesModule.f(anObject(it));
        var getOwnPropertySymbols = getOwnPropertySymbolsModule.f;
        return getOwnPropertySymbols ? concat(keys, getOwnPropertySymbols(it)) : keys;
      };
    }
  });

  // node_modules/core-js-pure/internals/copy-constructor-properties.js
  var require_copy_constructor_properties = __commonJS({
    "node_modules/core-js-pure/internals/copy-constructor-properties.js"(exports, module) {
      "use strict";
      var hasOwn = require_has_own_property();
      var ownKeys = require_own_keys();
      var getOwnPropertyDescriptorModule = require_object_get_own_property_descriptor();
      var definePropertyModule = require_object_define_property();
      module.exports = function(target, source, exceptions) {
        var keys = ownKeys(source);
        var defineProperty = definePropertyModule.f;
        var getOwnPropertyDescriptor = getOwnPropertyDescriptorModule.f;
        for (var i = 0; i < keys.length; i++) {
          var key = keys[i];
          if (!hasOwn(target, key) && !(exceptions && hasOwn(exceptions, key))) {
            defineProperty(target, key, getOwnPropertyDescriptor(source, key));
          }
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/install-error-cause.js
  var require_install_error_cause = __commonJS({
    "node_modules/core-js-pure/internals/install-error-cause.js"(exports, module) {
      "use strict";
      var isObject = require_is_object();
      var createNonEnumerableProperty = require_create_non_enumerable_property();
      module.exports = function(O, options) {
        if (isObject(options) && "cause" in options) {
          createNonEnumerableProperty(O, "cause", options.cause);
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/error-stack-clear.js
  var require_error_stack_clear = __commonJS({
    "node_modules/core-js-pure/internals/error-stack-clear.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var $Error = Error;
      var replace = uncurryThis("".replace);
      var TEST = (function(arg) {
        return String(new $Error(arg).stack);
      })("zxcasd");
      var V8_OR_CHAKRA_STACK_ENTRY = /\n\s*at [^:]*:[^\n]*/;
      var IS_V8_OR_CHAKRA_STACK = V8_OR_CHAKRA_STACK_ENTRY.test(TEST);
      module.exports = function(stack, dropEntries) {
        if (IS_V8_OR_CHAKRA_STACK && typeof stack == "string" && !$Error.prepareStackTrace) {
          while (dropEntries--) stack = replace(stack, V8_OR_CHAKRA_STACK_ENTRY, "");
        }
        return stack;
      };
    }
  });

  // node_modules/core-js-pure/internals/error-stack-installable.js
  var require_error_stack_installable = __commonJS({
    "node_modules/core-js-pure/internals/error-stack-installable.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      var createPropertyDescriptor = require_create_property_descriptor();
      module.exports = !fails(function() {
        var error = new Error("a");
        if (!("stack" in error)) return true;
        Object.defineProperty(error, "stack", createPropertyDescriptor(1, 7));
        return error.stack !== 7;
      });
    }
  });

  // node_modules/core-js-pure/internals/error-stack-install.js
  var require_error_stack_install = __commonJS({
    "node_modules/core-js-pure/internals/error-stack-install.js"(exports, module) {
      "use strict";
      var createNonEnumerableProperty = require_create_non_enumerable_property();
      var clearErrorStack = require_error_stack_clear();
      var ERROR_STACK_INSTALLABLE = require_error_stack_installable();
      var captureStackTrace = Error.captureStackTrace;
      module.exports = function(error, C, stack, dropEntries) {
        if (ERROR_STACK_INSTALLABLE) {
          if (captureStackTrace) captureStackTrace(error, C);
          else createNonEnumerableProperty(error, "stack", clearErrorStack(stack, dropEntries));
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/iterate.js
  var require_iterate = __commonJS({
    "node_modules/core-js-pure/internals/iterate.js"(exports, module) {
      "use strict";
      var bind = require_function_bind_context();
      var call = require_function_call();
      var anObject = require_an_object();
      var tryToString = require_try_to_string();
      var isArrayIteratorMethod = require_is_array_iterator_method();
      var lengthOfArrayLike = require_length_of_array_like();
      var isPrototypeOf = require_object_is_prototype_of();
      var getIterator = require_get_iterator();
      var getIteratorMethod = require_get_iterator_method();
      var iteratorClose = require_iterator_close();
      var $TypeError = TypeError;
      var Result = function(stopped, result) {
        this.stopped = stopped;
        this.result = result;
      };
      var ResultPrototype = Result.prototype;
      module.exports = function(iterable, unboundFunction, options) {
        var that = options && options.that;
        var AS_ENTRIES = !!(options && options.AS_ENTRIES);
        var IS_RECORD = !!(options && options.IS_RECORD);
        var IS_ITERATOR = !!(options && options.IS_ITERATOR);
        var INTERRUPTED = !!(options && options.INTERRUPTED);
        var fn = bind(unboundFunction, that);
        var iterator, iterFn, index, length, result, next, step;
        var stop = function(condition) {
          var $iterator = iterator;
          iterator = void 0;
          if ($iterator) iteratorClose($iterator, "normal");
          return new Result(true, condition);
        };
        var callFn = function(value2) {
          if (AS_ENTRIES) {
            anObject(value2);
            return INTERRUPTED ? fn(value2[0], value2[1], stop) : fn(value2[0], value2[1]);
          }
          return INTERRUPTED ? fn(value2, stop) : fn(value2);
        };
        if (IS_RECORD) {
          iterator = iterable.iterator;
        } else if (IS_ITERATOR) {
          iterator = iterable;
        } else {
          iterFn = getIteratorMethod(iterable);
          if (!iterFn) throw new $TypeError(tryToString(iterable) + " is not iterable");
          if (isArrayIteratorMethod(iterFn)) {
            for (index = 0, length = lengthOfArrayLike(iterable); length > index; index++) {
              result = callFn(iterable[index]);
              if (result && isPrototypeOf(ResultPrototype, result)) return result;
            }
            return new Result(false);
          }
          iterator = getIterator(iterable, iterFn);
        }
        next = IS_RECORD ? iterable.next : iterator.next;
        while (!(step = call(next, iterator)).done) {
          var value = step.value;
          try {
            result = callFn(value);
          } catch (error) {
            if (iterator) iteratorClose(iterator, "throw", error);
            else throw error;
          }
          if (typeof result == "object" && result && isPrototypeOf(ResultPrototype, result)) return result;
        }
        return new Result(false);
      };
    }
  });

  // node_modules/core-js-pure/internals/normalize-string-argument.js
  var require_normalize_string_argument = __commonJS({
    "node_modules/core-js-pure/internals/normalize-string-argument.js"(exports, module) {
      "use strict";
      var toString = require_to_string();
      module.exports = function(argument, $default) {
        return argument === void 0 ? arguments.length < 2 ? "" : $default : toString(argument);
      };
    }
  });

  // node_modules/core-js-pure/modules/es.aggregate-error.constructor.js
  var require_es_aggregate_error_constructor = __commonJS({
    "node_modules/core-js-pure/modules/es.aggregate-error.constructor.js"() {
      "use strict";
      var $ = require_export();
      var isPrototypeOf = require_object_is_prototype_of();
      var getPrototypeOf = require_object_get_prototype_of();
      var setPrototypeOf = require_object_set_prototype_of();
      var copyConstructorProperties = require_copy_constructor_properties();
      var create = require_object_create();
      var createNonEnumerableProperty = require_create_non_enumerable_property();
      var createPropertyDescriptor = require_create_property_descriptor();
      var installErrorCause = require_install_error_cause();
      var installErrorStack = require_error_stack_install();
      var iterate = require_iterate();
      var normalizeStringArgument = require_normalize_string_argument();
      var wellKnownSymbol = require_well_known_symbol();
      var TO_STRING_TAG = wellKnownSymbol("toStringTag");
      var $Error = Error;
      var push = [].push;
      var $AggregateError = function AggregateError(errors, message) {
        var isInstance = isPrototypeOf(AggregateErrorPrototype, this);
        var that;
        if (setPrototypeOf) {
          that = setPrototypeOf(new $Error(), isInstance ? getPrototypeOf(this) : AggregateErrorPrototype);
        } else {
          that = isInstance ? this : create(AggregateErrorPrototype);
          createNonEnumerableProperty(that, TO_STRING_TAG, "Error");
        }
        if (message !== void 0) createNonEnumerableProperty(that, "message", normalizeStringArgument(message));
        installErrorStack(that, $AggregateError, that.stack, 1);
        if (arguments.length > 2) installErrorCause(that, arguments[2]);
        var errorsArray = [];
        iterate(errors, push, { that: errorsArray });
        createNonEnumerableProperty(that, "errors", errorsArray);
        return that;
      };
      if (setPrototypeOf) setPrototypeOf($AggregateError, $Error);
      else copyConstructorProperties($AggregateError, $Error, { name: true });
      var AggregateErrorPrototype = $AggregateError.prototype = create($Error.prototype, {
        constructor: createPropertyDescriptor(1, $AggregateError),
        message: createPropertyDescriptor(1, ""),
        name: createPropertyDescriptor(1, "AggregateError")
      });
      $({ global: true, constructor: true, arity: 2 }, {
        AggregateError: $AggregateError
      });
    }
  });

  // node_modules/core-js-pure/modules/es.aggregate-error.js
  var require_es_aggregate_error = __commonJS({
    "node_modules/core-js-pure/modules/es.aggregate-error.js"() {
      "use strict";
      require_es_aggregate_error_constructor();
    }
  });

  // node_modules/core-js-pure/internals/environment.js
  var require_environment = __commonJS({
    "node_modules/core-js-pure/internals/environment.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var userAgent = require_environment_user_agent();
      var classof = require_classof_raw();
      var userAgentStartsWith = function(string) {
        return userAgent.slice(0, string.length) === string;
      };
      module.exports = (function() {
        if (userAgentStartsWith("Bun/")) return "BUN";
        if (userAgentStartsWith("Cloudflare-Workers")) return "CLOUDFLARE";
        if (userAgentStartsWith("Deno/")) return "DENO";
        if (userAgentStartsWith("Node.js/")) return "NODE";
        if (globalThis2.Bun && typeof Bun.version == "string") return "BUN";
        if (globalThis2.Deno && typeof Deno.version == "object") return "DENO";
        if (classof(globalThis2.process) === "process") return "NODE";
        if (globalThis2.window && globalThis2.document) return "BROWSER";
        return "REST";
      })();
    }
  });

  // node_modules/core-js-pure/internals/environment-is-node.js
  var require_environment_is_node = __commonJS({
    "node_modules/core-js-pure/internals/environment-is-node.js"(exports, module) {
      "use strict";
      var ENVIRONMENT = require_environment();
      module.exports = ENVIRONMENT === "NODE";
    }
  });

  // node_modules/core-js-pure/internals/set-species.js
  var require_set_species = __commonJS({
    "node_modules/core-js-pure/internals/set-species.js"(exports, module) {
      "use strict";
      var getBuiltIn = require_get_built_in();
      var defineBuiltInAccessor = require_define_built_in_accessor();
      var wellKnownSymbol = require_well_known_symbol();
      var DESCRIPTORS = require_descriptors();
      var SPECIES = wellKnownSymbol("species");
      module.exports = function(CONSTRUCTOR_NAME) {
        var Constructor = getBuiltIn(CONSTRUCTOR_NAME);
        if (DESCRIPTORS && Constructor && !Constructor[SPECIES]) {
          defineBuiltInAccessor(Constructor, SPECIES, {
            configurable: true,
            get: function() {
              return this;
            }
          });
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/an-instance.js
  var require_an_instance = __commonJS({
    "node_modules/core-js-pure/internals/an-instance.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var $TypeError = TypeError;
      module.exports = function(it, Prototype) {
        if (isPrototypeOf(Prototype, it)) return it;
        throw new $TypeError("Incorrect invocation");
      };
    }
  });

  // node_modules/core-js-pure/internals/a-constructor.js
  var require_a_constructor = __commonJS({
    "node_modules/core-js-pure/internals/a-constructor.js"(exports, module) {
      "use strict";
      var isConstructor = require_is_constructor();
      var tryToString = require_try_to_string();
      var $TypeError = TypeError;
      module.exports = function(argument) {
        if (isConstructor(argument)) return argument;
        throw new $TypeError(tryToString(argument) + " is not a constructor");
      };
    }
  });

  // node_modules/core-js-pure/internals/species-constructor.js
  var require_species_constructor = __commonJS({
    "node_modules/core-js-pure/internals/species-constructor.js"(exports, module) {
      "use strict";
      var anObject = require_an_object();
      var aConstructor = require_a_constructor();
      var isNullOrUndefined = require_is_null_or_undefined();
      var wellKnownSymbol = require_well_known_symbol();
      var SPECIES = wellKnownSymbol("species");
      module.exports = function(O, defaultConstructor) {
        var C = anObject(O).constructor;
        var S;
        return C === void 0 || isNullOrUndefined(S = anObject(C)[SPECIES]) ? defaultConstructor : aConstructor(S);
      };
    }
  });

  // node_modules/core-js-pure/internals/validate-arguments-length.js
  var require_validate_arguments_length = __commonJS({
    "node_modules/core-js-pure/internals/validate-arguments-length.js"(exports, module) {
      "use strict";
      var $TypeError = TypeError;
      module.exports = function(passed, required) {
        if (passed < required) throw new $TypeError("Not enough arguments");
        return passed;
      };
    }
  });

  // node_modules/core-js-pure/internals/environment-is-ios.js
  var require_environment_is_ios = __commonJS({
    "node_modules/core-js-pure/internals/environment-is-ios.js"(exports, module) {
      "use strict";
      var userAgent = require_environment_user_agent();
      module.exports = /ipad|iphone|ipod/i.test(userAgent) && /applewebkit/i.test(userAgent);
    }
  });

  // node_modules/core-js-pure/internals/task.js
  var require_task = __commonJS({
    "node_modules/core-js-pure/internals/task.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var apply = require_function_apply();
      var bind = require_function_bind_context();
      var isCallable = require_is_callable();
      var hasOwn = require_has_own_property();
      var fails = require_fails();
      var html = require_html();
      var arraySlice = require_array_slice();
      var createElement = require_document_create_element();
      var validateArgumentsLength = require_validate_arguments_length();
      var IS_IOS = require_environment_is_ios();
      var IS_NODE = require_environment_is_node();
      var set = globalThis2.setImmediate;
      var clear = globalThis2.clearImmediate;
      var process = globalThis2.process;
      var Dispatch = globalThis2.Dispatch;
      var Function2 = globalThis2.Function;
      var MessageChannel = globalThis2.MessageChannel;
      var String2 = globalThis2.String;
      var counter = 0;
      var queue = {};
      var ONREADYSTATECHANGE = "onreadystatechange";
      var $location;
      var defer;
      var channel;
      var port;
      fails(function() {
        $location = globalThis2.location;
      });
      var run = function(id) {
        if (hasOwn(queue, id)) {
          var fn = queue[id];
          delete queue[id];
          fn();
        }
      };
      var runner = function(id) {
        return function() {
          run(id);
        };
      };
      var eventListener = function(event) {
        run(event.data);
      };
      var globalPostMessageDefer = function(id) {
        globalThis2.postMessage(String2(id), $location.protocol + "//" + $location.host);
      };
      if (!set || !clear) {
        set = function setImmediate(handler) {
          validateArgumentsLength(arguments.length, 1);
          var fn = isCallable(handler) ? handler : Function2(handler);
          var args = arraySlice(arguments, 1);
          queue[++counter] = function() {
            apply(fn, void 0, args);
          };
          defer(counter);
          return counter;
        };
        clear = function clearImmediate(id) {
          delete queue[id];
        };
        if (IS_NODE) {
          defer = function(id) {
            process.nextTick(runner(id));
          };
        } else if (Dispatch && Dispatch.now) {
          defer = function(id) {
            Dispatch.now(runner(id));
          };
        } else if (MessageChannel && !IS_IOS) {
          channel = new MessageChannel();
          port = channel.port2;
          channel.port1.onmessage = eventListener;
          defer = bind(port.postMessage, port);
        } else if (globalThis2.addEventListener && isCallable(globalThis2.postMessage) && !globalThis2.importScripts && $location && $location.protocol !== "file:" && !fails(globalPostMessageDefer)) {
          defer = globalPostMessageDefer;
          globalThis2.addEventListener("message", eventListener, false);
        } else if (ONREADYSTATECHANGE in createElement("script")) {
          defer = function(id) {
            html.appendChild(createElement("script"))[ONREADYSTATECHANGE] = function() {
              html.removeChild(this);
              run(id);
            };
          };
        } else {
          defer = function(id) {
            setTimeout(runner(id), 0);
          };
        }
      }
      module.exports = {
        set,
        clear
      };
    }
  });

  // node_modules/core-js-pure/internals/safe-get-built-in.js
  var require_safe_get_built_in = __commonJS({
    "node_modules/core-js-pure/internals/safe-get-built-in.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var DESCRIPTORS = require_descriptors();
      var getOwnPropertyDescriptor = Object.getOwnPropertyDescriptor;
      module.exports = function(name) {
        if (!DESCRIPTORS) return globalThis2[name];
        var descriptor = getOwnPropertyDescriptor(globalThis2, name);
        return descriptor && descriptor.value;
      };
    }
  });

  // node_modules/core-js-pure/internals/queue.js
  var require_queue = __commonJS({
    "node_modules/core-js-pure/internals/queue.js"(exports, module) {
      "use strict";
      var Queue = function() {
        this.head = null;
        this.tail = null;
      };
      Queue.prototype = {
        add: function(item) {
          var entry = { item, next: null };
          var tail = this.tail;
          if (tail) tail.next = entry;
          else this.head = entry;
          this.tail = entry;
        },
        get: function() {
          var entry = this.head;
          if (entry) {
            var next = this.head = entry.next;
            if (next === null) this.tail = null;
            return entry.item;
          }
        }
      };
      module.exports = Queue;
    }
  });

  // node_modules/core-js-pure/internals/environment-is-ios-pebble.js
  var require_environment_is_ios_pebble = __commonJS({
    "node_modules/core-js-pure/internals/environment-is-ios-pebble.js"(exports, module) {
      "use strict";
      var userAgent = require_environment_user_agent();
      module.exports = /ipad|iphone|ipod/i.test(userAgent) && typeof Pebble != "undefined";
    }
  });

  // node_modules/core-js-pure/internals/environment-is-webos-webkit.js
  var require_environment_is_webos_webkit = __commonJS({
    "node_modules/core-js-pure/internals/environment-is-webos-webkit.js"(exports, module) {
      "use strict";
      var userAgent = require_environment_user_agent();
      module.exports = /web0s(?!.*chrome)/i.test(userAgent);
    }
  });

  // node_modules/core-js-pure/internals/microtask.js
  var require_microtask = __commonJS({
    "node_modules/core-js-pure/internals/microtask.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var safeGetBuiltIn = require_safe_get_built_in();
      var bind = require_function_bind_context();
      var macrotask = require_task().set;
      var Queue = require_queue();
      var IS_IOS = require_environment_is_ios();
      var IS_IOS_PEBBLE = require_environment_is_ios_pebble();
      var IS_WEBOS_WEBKIT = require_environment_is_webos_webkit();
      var IS_NODE = require_environment_is_node();
      var MutationObserver = globalThis2.MutationObserver || globalThis2.WebKitMutationObserver;
      var document2 = globalThis2.document;
      var process = globalThis2.process;
      var Promise2 = globalThis2.Promise;
      var microtask = safeGetBuiltIn("queueMicrotask");
      var notify;
      var toggle;
      var node;
      var promise;
      var then;
      if (!microtask) {
        queue = new Queue();
        flush = function() {
          var parent, fn;
          if (IS_NODE && (parent = process.domain)) parent.exit();
          while (fn = queue.get()) try {
            fn();
          } catch (error) {
            if (queue.head) notify();
            throw error;
          }
          if (parent) parent.enter();
        };
        if (!IS_IOS && !IS_NODE && !IS_WEBOS_WEBKIT && MutationObserver && document2) {
          toggle = true;
          node = document2.createTextNode("");
          new MutationObserver(flush).observe(node, { characterData: true });
          notify = function() {
            node.data = toggle = !toggle;
          };
        } else if (!IS_IOS_PEBBLE && Promise2 && Promise2.resolve) {
          promise = Promise2.resolve(void 0);
          promise.constructor = Promise2;
          then = bind(promise.then, promise);
          notify = function() {
            then(flush);
          };
        } else if (IS_NODE) {
          notify = function() {
            process.nextTick(flush);
          };
        } else {
          macrotask = bind(macrotask, globalThis2);
          notify = function() {
            macrotask(flush);
          };
        }
        microtask = function(fn) {
          if (!queue.head) notify();
          queue.add(fn);
        };
      }
      var queue;
      var flush;
      module.exports = microtask;
    }
  });

  // node_modules/core-js-pure/internals/host-report-errors.js
  var require_host_report_errors = __commonJS({
    "node_modules/core-js-pure/internals/host-report-errors.js"(exports, module) {
      "use strict";
      module.exports = function(a, b) {
        try {
          arguments.length === 1 ? console.error(a) : console.error(a, b);
        } catch (error) {
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/perform.js
  var require_perform = __commonJS({
    "node_modules/core-js-pure/internals/perform.js"(exports, module) {
      "use strict";
      module.exports = function(exec) {
        try {
          return { error: false, value: exec() };
        } catch (error) {
          return { error: true, value: error };
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/promise-native-constructor.js
  var require_promise_native_constructor = __commonJS({
    "node_modules/core-js-pure/internals/promise-native-constructor.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      module.exports = globalThis2.Promise;
    }
  });

  // node_modules/core-js-pure/internals/promise-constructor-detection.js
  var require_promise_constructor_detection = __commonJS({
    "node_modules/core-js-pure/internals/promise-constructor-detection.js"(exports, module) {
      "use strict";
      var globalThis2 = require_global_this();
      var NativePromiseConstructor = require_promise_native_constructor();
      var isCallable = require_is_callable();
      var isForced = require_is_forced();
      var inspectSource = require_inspect_source();
      var wellKnownSymbol = require_well_known_symbol();
      var ENVIRONMENT = require_environment();
      var IS_PURE = require_is_pure();
      var V8_VERSION = require_environment_v8_version();
      var NativePromisePrototype = NativePromiseConstructor && NativePromiseConstructor.prototype;
      var SPECIES = wellKnownSymbol("species");
      var SUBCLASSING = false;
      var NATIVE_PROMISE_REJECTION_EVENT = isCallable(globalThis2.PromiseRejectionEvent);
      var FORCED_PROMISE_CONSTRUCTOR = isForced("Promise", function() {
        var PROMISE_CONSTRUCTOR_SOURCE = inspectSource(NativePromiseConstructor);
        var GLOBAL_CORE_JS_PROMISE = PROMISE_CONSTRUCTOR_SOURCE !== String(NativePromiseConstructor);
        if (!GLOBAL_CORE_JS_PROMISE && V8_VERSION === 66) return true;
        if (IS_PURE && !(NativePromisePrototype["catch"] && NativePromisePrototype["finally"])) return true;
        if (!V8_VERSION || V8_VERSION < 51 || !/native code/.test(PROMISE_CONSTRUCTOR_SOURCE)) {
          var promise = new NativePromiseConstructor(function(resolve) {
            resolve(1);
          });
          var FakePromise = function(exec) {
            exec(function() {
            }, function() {
            });
          };
          var constructor = promise.constructor = {};
          constructor[SPECIES] = FakePromise;
          SUBCLASSING = promise.then(function() {
          }) instanceof FakePromise;
          if (!SUBCLASSING) return true;
        }
        return !GLOBAL_CORE_JS_PROMISE && (ENVIRONMENT === "BROWSER" || ENVIRONMENT === "DENO") && !NATIVE_PROMISE_REJECTION_EVENT;
      });
      module.exports = {
        CONSTRUCTOR: FORCED_PROMISE_CONSTRUCTOR,
        REJECTION_EVENT: NATIVE_PROMISE_REJECTION_EVENT,
        SUBCLASSING
      };
    }
  });

  // node_modules/core-js-pure/internals/new-promise-capability.js
  var require_new_promise_capability = __commonJS({
    "node_modules/core-js-pure/internals/new-promise-capability.js"(exports, module) {
      "use strict";
      var aCallable = require_a_callable();
      var $TypeError = TypeError;
      var PromiseCapability = function(C) {
        var resolve, reject;
        this.promise = new C(function($$resolve, $$reject) {
          if (resolve !== void 0 || reject !== void 0) throw new $TypeError("Bad Promise constructor");
          resolve = $$resolve;
          reject = $$reject;
        });
        this.resolve = aCallable(resolve);
        this.reject = aCallable(reject);
      };
      module.exports.f = function(C) {
        return new PromiseCapability(C);
      };
    }
  });

  // node_modules/core-js-pure/modules/es.promise.constructor.js
  var require_es_promise_constructor = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.constructor.js"() {
      "use strict";
      var $ = require_export();
      var IS_PURE = require_is_pure();
      var IS_NODE = require_environment_is_node();
      var globalThis2 = require_global_this();
      var path = require_path();
      var call = require_function_call();
      var defineBuiltIn = require_define_built_in();
      var setPrototypeOf = require_object_set_prototype_of();
      var setToStringTag = require_set_to_string_tag();
      var setSpecies = require_set_species();
      var aCallable = require_a_callable();
      var isCallable = require_is_callable();
      var isObject = require_is_object();
      var anInstance = require_an_instance();
      var speciesConstructor = require_species_constructor();
      var task = require_task().set;
      var microtask = require_microtask();
      var hostReportErrors = require_host_report_errors();
      var perform = require_perform();
      var Queue = require_queue();
      var InternalStateModule = require_internal_state();
      var NativePromiseConstructor = require_promise_native_constructor();
      var PromiseConstructorDetection = require_promise_constructor_detection();
      var newPromiseCapabilityModule = require_new_promise_capability();
      var PROMISE = "Promise";
      var FORCED_PROMISE_CONSTRUCTOR = PromiseConstructorDetection.CONSTRUCTOR;
      var NATIVE_PROMISE_REJECTION_EVENT = PromiseConstructorDetection.REJECTION_EVENT;
      var NATIVE_PROMISE_SUBCLASSING = PromiseConstructorDetection.SUBCLASSING;
      var getInternalPromiseState = InternalStateModule.getterFor(PROMISE);
      var setInternalState = InternalStateModule.set;
      var NativePromisePrototype = NativePromiseConstructor && NativePromiseConstructor.prototype;
      var PromiseConstructor = NativePromiseConstructor;
      var PromisePrototype = NativePromisePrototype;
      var TypeError2 = globalThis2.TypeError;
      var document2 = globalThis2.document;
      var process = globalThis2.process;
      var newPromiseCapability = newPromiseCapabilityModule.f;
      var newGenericPromiseCapability = newPromiseCapability;
      var DISPATCH_EVENT = !!(document2 && document2.createEvent && globalThis2.dispatchEvent);
      var UNHANDLED_REJECTION = "unhandledrejection";
      var REJECTION_HANDLED = "rejectionhandled";
      var PENDING = 0;
      var FULFILLED = 1;
      var REJECTED = 2;
      var HANDLED = 1;
      var UNHANDLED = 2;
      var Internal;
      var OwnPromiseCapability;
      var PromiseWrapper;
      var nativeThen;
      var isThenable = function(it) {
        var then;
        return isObject(it) && isCallable(then = it.then) ? then : false;
      };
      var callReaction = function(reaction, state) {
        var value = state.value;
        var ok = state.state === FULFILLED;
        var handler = ok ? reaction.ok : reaction.fail;
        var resolve = reaction.resolve;
        var reject = reaction.reject;
        var domain = reaction.domain;
        var result, then, exited;
        try {
          if (handler) {
            if (!ok) {
              if (state.rejection === UNHANDLED) onHandleUnhandled(state);
              state.rejection = HANDLED;
            }
            if (handler === true) result = value;
            else {
              if (domain) domain.enter();
              result = handler(value);
              if (domain) {
                domain.exit();
                exited = true;
              }
            }
            if (result === reaction.promise) {
              reject(new TypeError2("Promise-chain cycle"));
            } else if (then = isThenable(result)) {
              call(then, result, resolve, reject);
            } else resolve(result);
          } else reject(value);
        } catch (error) {
          if (domain && !exited) domain.exit();
          reject(error);
        }
      };
      var notify = function(state, isReject) {
        if (state.notified) return;
        state.notified = true;
        microtask(function() {
          var reactions = state.reactions;
          var reaction;
          while (reaction = reactions.get()) {
            callReaction(reaction, state);
          }
          state.notified = false;
          if (isReject && !state.rejection) onUnhandled(state);
        });
      };
      var dispatchEvent = function(name, promise, reason) {
        var event, handler;
        if (DISPATCH_EVENT) {
          event = document2.createEvent("Event");
          event.promise = promise;
          event.reason = reason;
          event.initEvent(name, false, true);
          globalThis2.dispatchEvent(event);
        } else event = { promise, reason };
        if (!NATIVE_PROMISE_REJECTION_EVENT && (handler = globalThis2["on" + name])) handler(event);
        else if (name === UNHANDLED_REJECTION) hostReportErrors("Unhandled promise rejection", reason);
      };
      var onUnhandled = function(state) {
        call(task, globalThis2, function() {
          var promise = state.facade;
          var value = state.value;
          var IS_UNHANDLED = isUnhandled(state);
          var result;
          if (IS_UNHANDLED) {
            result = perform(function() {
              if (IS_NODE) {
                process.emit("unhandledRejection", value, promise);
              } else dispatchEvent(UNHANDLED_REJECTION, promise, value);
            });
            state.rejection = IS_NODE || isUnhandled(state) ? UNHANDLED : HANDLED;
            if (result.error) throw result.value;
          }
        });
      };
      var isUnhandled = function(state) {
        return state.rejection !== HANDLED && !state.parent;
      };
      var onHandleUnhandled = function(state) {
        call(task, globalThis2, function() {
          var promise = state.facade;
          if (IS_NODE) {
            process.emit("rejectionHandled", promise);
          } else dispatchEvent(REJECTION_HANDLED, promise, state.value);
        });
      };
      var bind = function(fn, state, unwrap) {
        return function(value) {
          fn(state, value, unwrap);
        };
      };
      var internalReject = function(state, value, unwrap) {
        if (state.done) return;
        state.done = true;
        if (unwrap) state = unwrap;
        state.value = value;
        state.state = REJECTED;
        notify(state, true);
      };
      var internalResolve = function(state, value, unwrap) {
        if (state.done) return;
        state.done = true;
        if (unwrap) state = unwrap;
        try {
          if (state.facade === value) throw new TypeError2("Promise can't be resolved itself");
          var then = isThenable(value);
          if (then) {
            microtask(function() {
              var wrapper = { done: false };
              try {
                call(
                  then,
                  value,
                  bind(internalResolve, wrapper, state),
                  bind(internalReject, wrapper, state)
                );
              } catch (error) {
                internalReject(wrapper, error, state);
              }
            });
          } else {
            state.value = value;
            state.state = FULFILLED;
            notify(state, false);
          }
        } catch (error) {
          internalReject({ done: false }, error, state);
        }
      };
      if (FORCED_PROMISE_CONSTRUCTOR) {
        PromiseConstructor = function Promise2(executor) {
          anInstance(this, PromisePrototype);
          aCallable(executor);
          call(Internal, this);
          var state = getInternalPromiseState(this);
          try {
            executor(bind(internalResolve, state), bind(internalReject, state));
          } catch (error) {
            internalReject(state, error);
          }
        };
        PromisePrototype = PromiseConstructor.prototype;
        Internal = function Promise2(executor) {
          setInternalState(this, {
            type: PROMISE,
            done: false,
            notified: false,
            parent: false,
            reactions: new Queue(),
            rejection: false,
            state: PENDING,
            value: null
          });
        };
        Internal.prototype = defineBuiltIn(PromisePrototype, "then", function then(onFulfilled, onRejected) {
          var state = getInternalPromiseState(this);
          var reaction = newPromiseCapability(speciesConstructor(this, PromiseConstructor));
          state.parent = true;
          reaction.ok = isCallable(onFulfilled) ? onFulfilled : true;
          reaction.fail = isCallable(onRejected) && onRejected;
          reaction.domain = IS_NODE ? process.domain : void 0;
          if (state.state === PENDING) state.reactions.add(reaction);
          else microtask(function() {
            callReaction(reaction, state);
          });
          return reaction.promise;
        });
        OwnPromiseCapability = function() {
          var promise = new Internal();
          var state = getInternalPromiseState(promise);
          this.promise = promise;
          this.resolve = bind(internalResolve, state);
          this.reject = bind(internalReject, state);
        };
        newPromiseCapabilityModule.f = newPromiseCapability = function(C) {
          return C === PromiseConstructor || C === PromiseWrapper ? new OwnPromiseCapability(C) : newGenericPromiseCapability(C);
        };
        if (!IS_PURE && isCallable(NativePromiseConstructor) && NativePromisePrototype !== Object.prototype) {
          nativeThen = NativePromisePrototype.then;
          if (!NATIVE_PROMISE_SUBCLASSING) {
            defineBuiltIn(NativePromisePrototype, "then", function then(onFulfilled, onRejected) {
              var that = this;
              return new PromiseConstructor(function(resolve, reject) {
                call(nativeThen, that, resolve, reject);
              }).then(onFulfilled, onRejected);
            }, { unsafe: true });
          }
          try {
            delete NativePromisePrototype.constructor;
          } catch (error) {
          }
          if (setPrototypeOf) {
            setPrototypeOf(NativePromisePrototype, PromisePrototype);
          }
        }
      }
      $({ global: true, constructor: true, wrap: true, forced: FORCED_PROMISE_CONSTRUCTOR }, {
        Promise: PromiseConstructor
      });
      PromiseWrapper = path.Promise;
      setToStringTag(PromiseConstructor, PROMISE, false, true);
      setSpecies(PROMISE);
    }
  });

  // node_modules/core-js-pure/internals/promise-statics-incorrect-iteration.js
  var require_promise_statics_incorrect_iteration = __commonJS({
    "node_modules/core-js-pure/internals/promise-statics-incorrect-iteration.js"(exports, module) {
      "use strict";
      var NativePromiseConstructor = require_promise_native_constructor();
      var checkCorrectnessOfIteration = require_check_correctness_of_iteration();
      var FORCED_PROMISE_CONSTRUCTOR = require_promise_constructor_detection().CONSTRUCTOR;
      module.exports = FORCED_PROMISE_CONSTRUCTOR || !checkCorrectnessOfIteration(function(iterable) {
        NativePromiseConstructor.all(iterable).then(void 0, function() {
        });
      });
    }
  });

  // node_modules/core-js-pure/modules/es.promise.all.js
  var require_es_promise_all = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.all.js"() {
      "use strict";
      var $ = require_export();
      var call = require_function_call();
      var aCallable = require_a_callable();
      var newPromiseCapabilityModule = require_new_promise_capability();
      var perform = require_perform();
      var iterate = require_iterate();
      var PROMISE_STATICS_INCORRECT_ITERATION = require_promise_statics_incorrect_iteration();
      $({ target: "Promise", stat: true, forced: PROMISE_STATICS_INCORRECT_ITERATION }, {
        all: function all(iterable) {
          var C = this;
          var capability = newPromiseCapabilityModule.f(C);
          var resolve = capability.resolve;
          var reject = capability.reject;
          var result = perform(function() {
            var $promiseResolve = aCallable(C.resolve);
            var values = [];
            var counter = 0;
            var remaining = 1;
            iterate(iterable, function(promise) {
              var index = counter++;
              var alreadyCalled = false;
              remaining++;
              call($promiseResolve, C, promise).then(function(value) {
                if (alreadyCalled) return;
                alreadyCalled = true;
                values[index] = value;
                --remaining || resolve(values);
              }, reject);
            });
            --remaining || resolve(values);
          });
          if (result.error) reject(result.value);
          return capability.promise;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.promise.catch.js
  var require_es_promise_catch = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.catch.js"() {
      "use strict";
      var $ = require_export();
      var IS_PURE = require_is_pure();
      var FORCED_PROMISE_CONSTRUCTOR = require_promise_constructor_detection().CONSTRUCTOR;
      var NativePromiseConstructor = require_promise_native_constructor();
      var getBuiltIn = require_get_built_in();
      var isCallable = require_is_callable();
      var defineBuiltIn = require_define_built_in();
      var NativePromisePrototype = NativePromiseConstructor && NativePromiseConstructor.prototype;
      $({ target: "Promise", proto: true, forced: FORCED_PROMISE_CONSTRUCTOR, real: true }, {
        "catch": function(onRejected) {
          return this.then(void 0, onRejected);
        }
      });
      if (!IS_PURE && isCallable(NativePromiseConstructor)) {
        method = getBuiltIn("Promise").prototype["catch"];
        if (NativePromisePrototype["catch"] !== method) {
          defineBuiltIn(NativePromisePrototype, "catch", method, { unsafe: true });
        }
      }
      var method;
    }
  });

  // node_modules/core-js-pure/modules/es.promise.race.js
  var require_es_promise_race = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.race.js"() {
      "use strict";
      var $ = require_export();
      var call = require_function_call();
      var aCallable = require_a_callable();
      var newPromiseCapabilityModule = require_new_promise_capability();
      var perform = require_perform();
      var iterate = require_iterate();
      var PROMISE_STATICS_INCORRECT_ITERATION = require_promise_statics_incorrect_iteration();
      $({ target: "Promise", stat: true, forced: PROMISE_STATICS_INCORRECT_ITERATION }, {
        race: function race(iterable) {
          var C = this;
          var capability = newPromiseCapabilityModule.f(C);
          var reject = capability.reject;
          var result = perform(function() {
            var $promiseResolve = aCallable(C.resolve);
            iterate(iterable, function(promise) {
              call($promiseResolve, C, promise).then(capability.resolve, reject);
            });
          });
          if (result.error) reject(result.value);
          return capability.promise;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.promise.reject.js
  var require_es_promise_reject = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.reject.js"() {
      "use strict";
      var $ = require_export();
      var newPromiseCapabilityModule = require_new_promise_capability();
      var FORCED_PROMISE_CONSTRUCTOR = require_promise_constructor_detection().CONSTRUCTOR;
      $({ target: "Promise", stat: true, forced: FORCED_PROMISE_CONSTRUCTOR }, {
        reject: function reject(r) {
          var capability = newPromiseCapabilityModule.f(this);
          var capabilityReject = capability.reject;
          capabilityReject(r);
          return capability.promise;
        }
      });
    }
  });

  // node_modules/core-js-pure/internals/promise-resolve.js
  var require_promise_resolve = __commonJS({
    "node_modules/core-js-pure/internals/promise-resolve.js"(exports, module) {
      "use strict";
      var anObject = require_an_object();
      var isObject = require_is_object();
      var newPromiseCapability = require_new_promise_capability();
      module.exports = function(C, x) {
        anObject(C);
        if (isObject(x) && x.constructor === C) return x;
        var promiseCapability = newPromiseCapability.f(C);
        var resolve = promiseCapability.resolve;
        resolve(x);
        return promiseCapability.promise;
      };
    }
  });

  // node_modules/core-js-pure/modules/es.promise.resolve.js
  var require_es_promise_resolve = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.resolve.js"() {
      "use strict";
      var $ = require_export();
      var getBuiltIn = require_get_built_in();
      var IS_PURE = require_is_pure();
      var NativePromiseConstructor = require_promise_native_constructor();
      var FORCED_PROMISE_CONSTRUCTOR = require_promise_constructor_detection().CONSTRUCTOR;
      var promiseResolve = require_promise_resolve();
      var PromiseConstructorWrapper = getBuiltIn("Promise");
      var CHECK_WRAPPER = IS_PURE && !FORCED_PROMISE_CONSTRUCTOR;
      $({ target: "Promise", stat: true, forced: IS_PURE || FORCED_PROMISE_CONSTRUCTOR }, {
        resolve: function resolve(x) {
          return promiseResolve(CHECK_WRAPPER && this === PromiseConstructorWrapper ? NativePromiseConstructor : this, x);
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.promise.js
  var require_es_promise = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.js"() {
      "use strict";
      require_es_promise_constructor();
      require_es_promise_all();
      require_es_promise_catch();
      require_es_promise_race();
      require_es_promise_reject();
      require_es_promise_resolve();
    }
  });

  // node_modules/core-js-pure/modules/es.promise.all-settled.js
  var require_es_promise_all_settled = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.all-settled.js"() {
      "use strict";
      var $ = require_export();
      var call = require_function_call();
      var aCallable = require_a_callable();
      var newPromiseCapabilityModule = require_new_promise_capability();
      var perform = require_perform();
      var iterate = require_iterate();
      var PROMISE_STATICS_INCORRECT_ITERATION = require_promise_statics_incorrect_iteration();
      $({ target: "Promise", stat: true, forced: PROMISE_STATICS_INCORRECT_ITERATION }, {
        allSettled: function allSettled(iterable) {
          var C = this;
          var capability = newPromiseCapabilityModule.f(C);
          var resolve = capability.resolve;
          var reject = capability.reject;
          var result = perform(function() {
            var promiseResolve = aCallable(C.resolve);
            var values = [];
            var counter = 0;
            var remaining = 1;
            iterate(iterable, function(promise) {
              var index = counter++;
              var alreadyCalled = false;
              remaining++;
              call(promiseResolve, C, promise).then(function(value) {
                if (alreadyCalled) return;
                alreadyCalled = true;
                values[index] = { status: "fulfilled", value };
                --remaining || resolve(values);
              }, function(error) {
                if (alreadyCalled) return;
                alreadyCalled = true;
                values[index] = { status: "rejected", reason: error };
                --remaining || resolve(values);
              });
            });
            --remaining || resolve(values);
          });
          if (result.error) reject(result.value);
          return capability.promise;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.promise.any.js
  var require_es_promise_any = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.any.js"() {
      "use strict";
      var $ = require_export();
      var call = require_function_call();
      var aCallable = require_a_callable();
      var getBuiltIn = require_get_built_in();
      var newPromiseCapabilityModule = require_new_promise_capability();
      var perform = require_perform();
      var iterate = require_iterate();
      var PROMISE_STATICS_INCORRECT_ITERATION = require_promise_statics_incorrect_iteration();
      var PROMISE_ANY_ERROR = "No one promise resolved";
      $({ target: "Promise", stat: true, forced: PROMISE_STATICS_INCORRECT_ITERATION }, {
        any: function any(iterable) {
          var C = this;
          var AggregateError = getBuiltIn("AggregateError");
          var capability = newPromiseCapabilityModule.f(C);
          var resolve = capability.resolve;
          var reject = capability.reject;
          var result = perform(function() {
            var promiseResolve = aCallable(C.resolve);
            var errors = [];
            var counter = 0;
            var remaining = 1;
            var alreadyResolved = false;
            iterate(iterable, function(promise) {
              var index = counter++;
              var alreadyRejected = false;
              remaining++;
              call(promiseResolve, C, promise).then(function(value) {
                if (alreadyRejected || alreadyResolved) return;
                alreadyResolved = true;
                resolve(value);
              }, function(error) {
                if (alreadyRejected || alreadyResolved) return;
                alreadyRejected = true;
                errors[index] = error;
                --remaining || reject(new AggregateError(errors, PROMISE_ANY_ERROR));
              });
            });
            --remaining || reject(new AggregateError(errors, PROMISE_ANY_ERROR));
          });
          if (result.error) reject(result.value);
          return capability.promise;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.promise.try.js
  var require_es_promise_try = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.try.js"() {
      "use strict";
      var $ = require_export();
      var globalThis2 = require_global_this();
      var apply = require_function_apply();
      var slice = require_array_slice();
      var newPromiseCapabilityModule = require_new_promise_capability();
      var aCallable = require_a_callable();
      var perform = require_perform();
      var Promise2 = globalThis2.Promise;
      var ACCEPT_ARGUMENTS = false;
      var FORCED = !Promise2 || !Promise2["try"] || perform(function() {
        Promise2["try"](function(argument) {
          ACCEPT_ARGUMENTS = argument === 8;
        }, 8);
      }).error || !ACCEPT_ARGUMENTS;
      $({ target: "Promise", stat: true, forced: FORCED }, {
        "try": function(callbackfn) {
          var args = arguments.length > 1 ? slice(arguments, 1) : [];
          var promiseCapability = newPromiseCapabilityModule.f(this);
          var result = perform(function() {
            return apply(aCallable(callbackfn), void 0, args);
          });
          (result.error ? promiseCapability.reject : promiseCapability.resolve)(result.value);
          return promiseCapability.promise;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.promise.with-resolvers.js
  var require_es_promise_with_resolvers = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.with-resolvers.js"() {
      "use strict";
      var $ = require_export();
      var newPromiseCapabilityModule = require_new_promise_capability();
      $({ target: "Promise", stat: true }, {
        withResolvers: function withResolvers() {
          var promiseCapability = newPromiseCapabilityModule.f(this);
          return {
            promise: promiseCapability.promise,
            resolve: promiseCapability.resolve,
            reject: promiseCapability.reject
          };
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.promise.finally.js
  var require_es_promise_finally = __commonJS({
    "node_modules/core-js-pure/modules/es.promise.finally.js"() {
      "use strict";
      var $ = require_export();
      var IS_PURE = require_is_pure();
      var NativePromiseConstructor = require_promise_native_constructor();
      var fails = require_fails();
      var getBuiltIn = require_get_built_in();
      var isCallable = require_is_callable();
      var speciesConstructor = require_species_constructor();
      var promiseResolve = require_promise_resolve();
      var defineBuiltIn = require_define_built_in();
      var NativePromisePrototype = NativePromiseConstructor && NativePromiseConstructor.prototype;
      var NON_GENERIC = !!NativePromiseConstructor && fails(function() {
        NativePromisePrototype["finally"].call({ then: function() {
        } }, function() {
        });
      });
      $({ target: "Promise", proto: true, real: true, forced: NON_GENERIC }, {
        "finally": function(onFinally) {
          var C = speciesConstructor(this, getBuiltIn("Promise"));
          var isFunction = isCallable(onFinally);
          return this.then(
            isFunction ? function(x) {
              return promiseResolve(C, onFinally()).then(function() {
                return x;
              });
            } : onFinally,
            isFunction ? function(e) {
              return promiseResolve(C, onFinally()).then(function() {
                throw e;
              });
            } : onFinally
          );
        }
      });
      if (!IS_PURE && isCallable(NativePromiseConstructor)) {
        method = getBuiltIn("Promise").prototype["finally"];
        if (NativePromisePrototype["finally"] !== method) {
          defineBuiltIn(NativePromisePrototype, "finally", method, { unsafe: true });
        }
      }
      var method;
    }
  });

  // node_modules/core-js-pure/es/promise/index.js
  var require_promise = __commonJS({
    "node_modules/core-js-pure/es/promise/index.js"(exports, module) {
      "use strict";
      require_es_aggregate_error();
      require_es_array_iterator();
      require_es_object_to_string();
      require_es_promise();
      require_es_promise_all_settled();
      require_es_promise_any();
      require_es_promise_try();
      require_es_promise_with_resolvers();
      require_es_promise_finally();
      require_es_string_iterator();
      var path = require_path();
      module.exports = path.Promise;
    }
  });

  // node_modules/core-js-pure/stable/promise/index.js
  var require_promise2 = __commonJS({
    "node_modules/core-js-pure/stable/promise/index.js"(exports, module) {
      "use strict";
      var parent = require_promise();
      require_web_dom_collections_iterator();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/modules/esnext.promise.try.js
  var require_esnext_promise_try = __commonJS({
    "node_modules/core-js-pure/modules/esnext.promise.try.js"() {
      "use strict";
      require_es_promise_try();
    }
  });

  // node_modules/core-js-pure/modules/esnext.promise.with-resolvers.js
  var require_esnext_promise_with_resolvers = __commonJS({
    "node_modules/core-js-pure/modules/esnext.promise.with-resolvers.js"() {
      "use strict";
      require_es_promise_with_resolvers();
    }
  });

  // node_modules/core-js-pure/actual/promise/index.js
  var require_promise3 = __commonJS({
    "node_modules/core-js-pure/actual/promise/index.js"(exports, module) {
      "use strict";
      var parent = require_promise2();
      require_esnext_promise_try();
      require_esnext_promise_with_resolvers();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/modules/esnext.aggregate-error.js
  var require_esnext_aggregate_error = __commonJS({
    "node_modules/core-js-pure/modules/esnext.aggregate-error.js"() {
      "use strict";
      require_es_aggregate_error();
    }
  });

  // node_modules/core-js-pure/modules/esnext.promise.all-settled.js
  var require_esnext_promise_all_settled = __commonJS({
    "node_modules/core-js-pure/modules/esnext.promise.all-settled.js"() {
      "use strict";
      require_es_promise_all_settled();
    }
  });

  // node_modules/core-js-pure/modules/esnext.promise.any.js
  var require_esnext_promise_any = __commonJS({
    "node_modules/core-js-pure/modules/esnext.promise.any.js"() {
      "use strict";
      require_es_promise_any();
    }
  });

  // node_modules/core-js-pure/full/promise/index.js
  var require_promise4 = __commonJS({
    "node_modules/core-js-pure/full/promise/index.js"(exports, module) {
      "use strict";
      var parent = require_promise3();
      require_esnext_aggregate_error();
      require_esnext_promise_all_settled();
      require_esnext_promise_any();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/promise/index.js
  var require_promise5 = __commonJS({
    "node_modules/core-js-pure/features/promise/index.js"(exports, module) {
      "use strict";
      module.exports = require_promise4();
    }
  });

  // node_modules/core-js-pure/es/symbol/async-iterator.js
  var require_async_iterator = __commonJS({
    "node_modules/core-js-pure/es/symbol/async-iterator.js"(exports, module) {
      "use strict";
      require_es_symbol_async_iterator();
      var WrappedWellKnownSymbolModule = require_well_known_symbol_wrapped();
      module.exports = WrappedWellKnownSymbolModule.f("asyncIterator");
    }
  });

  // node_modules/core-js-pure/stable/symbol/async-iterator.js
  var require_async_iterator2 = __commonJS({
    "node_modules/core-js-pure/stable/symbol/async-iterator.js"(exports, module) {
      "use strict";
      var parent = require_async_iterator();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/symbol/async-iterator.js
  var require_async_iterator3 = __commonJS({
    "node_modules/core-js-pure/actual/symbol/async-iterator.js"(exports, module) {
      "use strict";
      var parent = require_async_iterator2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/symbol/async-iterator.js
  var require_async_iterator4 = __commonJS({
    "node_modules/core-js-pure/full/symbol/async-iterator.js"(exports, module) {
      "use strict";
      var parent = require_async_iterator3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/symbol/async-iterator.js
  var require_async_iterator5 = __commonJS({
    "node_modules/core-js-pure/features/symbol/async-iterator.js"(exports, module) {
      "use strict";
      module.exports = require_async_iterator4();
    }
  });

  // node_modules/core-js-pure/modules/es.object.get-prototype-of.js
  var require_es_object_get_prototype_of = __commonJS({
    "node_modules/core-js-pure/modules/es.object.get-prototype-of.js"() {
      "use strict";
      var $ = require_export();
      var fails = require_fails();
      var toObject = require_to_object();
      var nativeGetPrototypeOf = require_object_get_prototype_of();
      var CORRECT_PROTOTYPE_GETTER = require_correct_prototype_getter();
      var FAILS_ON_PRIMITIVES = fails(function() {
        nativeGetPrototypeOf(1);
      });
      $({ target: "Object", stat: true, forced: FAILS_ON_PRIMITIVES, sham: !CORRECT_PROTOTYPE_GETTER }, {
        getPrototypeOf: function getPrototypeOf(it) {
          return nativeGetPrototypeOf(toObject(it));
        }
      });
    }
  });

  // node_modules/core-js-pure/es/object/get-prototype-of.js
  var require_get_prototype_of = __commonJS({
    "node_modules/core-js-pure/es/object/get-prototype-of.js"(exports, module) {
      "use strict";
      require_es_object_get_prototype_of();
      var path = require_path();
      module.exports = path.Object.getPrototypeOf;
    }
  });

  // node_modules/core-js-pure/stable/object/get-prototype-of.js
  var require_get_prototype_of2 = __commonJS({
    "node_modules/core-js-pure/stable/object/get-prototype-of.js"(exports, module) {
      "use strict";
      var parent = require_get_prototype_of();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/object/get-prototype-of.js
  var require_get_prototype_of3 = __commonJS({
    "node_modules/core-js-pure/actual/object/get-prototype-of.js"(exports, module) {
      "use strict";
      var parent = require_get_prototype_of2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/object/get-prototype-of.js
  var require_get_prototype_of4 = __commonJS({
    "node_modules/core-js-pure/full/object/get-prototype-of.js"(exports, module) {
      "use strict";
      var parent = require_get_prototype_of3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/object/get-prototype-of.js
  var require_get_prototype_of5 = __commonJS({
    "node_modules/core-js-pure/features/object/get-prototype-of.js"(exports, module) {
      "use strict";
      module.exports = require_get_prototype_of4();
    }
  });

  // node_modules/core-js-pure/modules/es.array.reverse.js
  var require_es_array_reverse = __commonJS({
    "node_modules/core-js-pure/modules/es.array.reverse.js"() {
      "use strict";
      var $ = require_export();
      var uncurryThis = require_function_uncurry_this();
      var isArray = require_is_array();
      var nativeReverse = uncurryThis([].reverse);
      var test = [1, 2];
      $({ target: "Array", proto: true, forced: String(test) === String(test.reverse()) }, {
        reverse: function reverse() {
          if (isArray(this)) this.length = this.length;
          return nativeReverse(this);
        }
      });
    }
  });

  // node_modules/core-js-pure/es/array/virtual/reverse.js
  var require_reverse = __commonJS({
    "node_modules/core-js-pure/es/array/virtual/reverse.js"(exports, module) {
      "use strict";
      require_es_array_reverse();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Array", "reverse");
    }
  });

  // node_modules/core-js-pure/es/instance/reverse.js
  var require_reverse2 = __commonJS({
    "node_modules/core-js-pure/es/instance/reverse.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_reverse();
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        var own = it.reverse;
        return it === ArrayPrototype || isPrototypeOf(ArrayPrototype, it) && own === ArrayPrototype.reverse ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/reverse.js
  var require_reverse3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/reverse.js"(exports, module) {
      "use strict";
      var parent = require_reverse2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/reverse.js
  var require_reverse4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/reverse.js"(exports, module) {
      "use strict";
      var parent = require_reverse3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/reverse.js
  var require_reverse5 = __commonJS({
    "node_modules/core-js-pure/full/instance/reverse.js"(exports, module) {
      "use strict";
      var parent = require_reverse4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/reverse.js
  var require_reverse6 = __commonJS({
    "node_modules/core-js-pure/features/instance/reverse.js"(exports, module) {
      "use strict";
      module.exports = require_reverse5();
    }
  });

  // node_modules/@babel/runtime-corejs3/helpers/OverloadYield.js
  var require_OverloadYield = __commonJS({
    "node_modules/@babel/runtime-corejs3/helpers/OverloadYield.js"(exports, module) {
      function _OverloadYield2(e, d) {
        this.v = e, this.k = d;
      }
      module.exports = _OverloadYield2, module.exports.__esModule = true, module.exports["default"] = module.exports;
    }
  });

  // node_modules/core-js-pure/modules/es.object.create.js
  var require_es_object_create = __commonJS({
    "node_modules/core-js-pure/modules/es.object.create.js"() {
      "use strict";
      var $ = require_export();
      var DESCRIPTORS = require_descriptors();
      var create = require_object_create();
      $({ target: "Object", stat: true, sham: !DESCRIPTORS }, {
        create
      });
    }
  });

  // node_modules/core-js-pure/es/object/create.js
  var require_create = __commonJS({
    "node_modules/core-js-pure/es/object/create.js"(exports, module) {
      "use strict";
      require_es_object_create();
      var path = require_path();
      var Object2 = path.Object;
      module.exports = function create(P, D) {
        return Object2.create(P, D);
      };
    }
  });

  // node_modules/core-js-pure/stable/object/create.js
  var require_create2 = __commonJS({
    "node_modules/core-js-pure/stable/object/create.js"(exports, module) {
      "use strict";
      var parent = require_create();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/object/create.js
  var require_create3 = __commonJS({
    "node_modules/core-js-pure/actual/object/create.js"(exports, module) {
      "use strict";
      var parent = require_create2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/object/create.js
  var require_create4 = __commonJS({
    "node_modules/core-js-pure/full/object/create.js"(exports, module) {
      "use strict";
      var parent = require_create3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/object/create.js
  var require_create5 = __commonJS({
    "node_modules/core-js-pure/features/object/create.js"(exports, module) {
      "use strict";
      module.exports = require_create4();
    }
  });

  // node_modules/core-js-pure/internals/function-bind.js
  var require_function_bind = __commonJS({
    "node_modules/core-js-pure/internals/function-bind.js"(exports, module) {
      "use strict";
      var uncurryThis = require_function_uncurry_this();
      var aCallable = require_a_callable();
      var isObject = require_is_object();
      var hasOwn = require_has_own_property();
      var arraySlice = require_array_slice();
      var NATIVE_BIND = require_function_bind_native();
      var $Function = Function;
      var concat = uncurryThis([].concat);
      var join = uncurryThis([].join);
      var factories = {};
      var construct = function(C, argsLength, args) {
        if (!hasOwn(factories, argsLength)) {
          var list = [];
          var i = 0;
          for (; i < argsLength; i++) list[i] = "a[" + i + "]";
          factories[argsLength] = $Function("C,a", "return new C(" + join(list, ",") + ")");
        }
        return factories[argsLength](C, args);
      };
      module.exports = NATIVE_BIND ? $Function.bind : function bind(that) {
        var F = aCallable(this);
        var Prototype = F.prototype;
        var partArgs = arraySlice(arguments, 1);
        var boundFunction = function bound() {
          var args = concat(partArgs, arraySlice(arguments));
          return this instanceof boundFunction ? construct(F, args.length, args) : F.apply(that, args);
        };
        if (isObject(Prototype)) boundFunction.prototype = Prototype;
        return boundFunction;
      };
    }
  });

  // node_modules/core-js-pure/modules/es.function.bind.js
  var require_es_function_bind = __commonJS({
    "node_modules/core-js-pure/modules/es.function.bind.js"() {
      "use strict";
      var $ = require_export();
      var bind = require_function_bind();
      $({ target: "Function", proto: true, forced: Function.bind !== bind }, {
        bind
      });
    }
  });

  // node_modules/core-js-pure/es/function/virtual/bind.js
  var require_bind = __commonJS({
    "node_modules/core-js-pure/es/function/virtual/bind.js"(exports, module) {
      "use strict";
      require_es_function_bind();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Function", "bind");
    }
  });

  // node_modules/core-js-pure/es/instance/bind.js
  var require_bind2 = __commonJS({
    "node_modules/core-js-pure/es/instance/bind.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_bind();
      var FunctionPrototype = Function.prototype;
      module.exports = function(it) {
        var own = it.bind;
        return it === FunctionPrototype || isPrototypeOf(FunctionPrototype, it) && own === FunctionPrototype.bind ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/bind.js
  var require_bind3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/bind.js"(exports, module) {
      "use strict";
      var parent = require_bind2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/bind.js
  var require_bind4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/bind.js"(exports, module) {
      "use strict";
      var parent = require_bind3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/bind.js
  var require_bind5 = __commonJS({
    "node_modules/core-js-pure/full/instance/bind.js"(exports, module) {
      "use strict";
      var parent = require_bind4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/bind.js
  var require_bind6 = __commonJS({
    "node_modules/core-js-pure/features/instance/bind.js"(exports, module) {
      "use strict";
      module.exports = require_bind5();
    }
  });

  // node_modules/core-js-pure/modules/es.object.set-prototype-of.js
  var require_es_object_set_prototype_of = __commonJS({
    "node_modules/core-js-pure/modules/es.object.set-prototype-of.js"() {
      "use strict";
      var $ = require_export();
      var setPrototypeOf = require_object_set_prototype_of();
      $({ target: "Object", stat: true }, {
        setPrototypeOf
      });
    }
  });

  // node_modules/core-js-pure/es/object/set-prototype-of.js
  var require_set_prototype_of = __commonJS({
    "node_modules/core-js-pure/es/object/set-prototype-of.js"(exports, module) {
      "use strict";
      require_es_object_set_prototype_of();
      var path = require_path();
      module.exports = path.Object.setPrototypeOf;
    }
  });

  // node_modules/core-js-pure/stable/object/set-prototype-of.js
  var require_set_prototype_of2 = __commonJS({
    "node_modules/core-js-pure/stable/object/set-prototype-of.js"(exports, module) {
      "use strict";
      var parent = require_set_prototype_of();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/object/set-prototype-of.js
  var require_set_prototype_of3 = __commonJS({
    "node_modules/core-js-pure/actual/object/set-prototype-of.js"(exports, module) {
      "use strict";
      var parent = require_set_prototype_of2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/object/set-prototype-of.js
  var require_set_prototype_of4 = __commonJS({
    "node_modules/core-js-pure/full/object/set-prototype-of.js"(exports, module) {
      "use strict";
      var parent = require_set_prototype_of3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/object/set-prototype-of.js
  var require_set_prototype_of5 = __commonJS({
    "node_modules/core-js-pure/features/object/set-prototype-of.js"(exports, module) {
      "use strict";
      module.exports = require_set_prototype_of4();
    }
  });

  // node_modules/core-js-pure/modules/es.object.define-property.js
  var require_es_object_define_property = __commonJS({
    "node_modules/core-js-pure/modules/es.object.define-property.js"() {
      "use strict";
      var $ = require_export();
      var DESCRIPTORS = require_descriptors();
      var defineProperty = require_object_define_property().f;
      $({ target: "Object", stat: true, forced: Object.defineProperty !== defineProperty, sham: !DESCRIPTORS }, {
        defineProperty
      });
    }
  });

  // node_modules/core-js-pure/es/object/define-property.js
  var require_define_property = __commonJS({
    "node_modules/core-js-pure/es/object/define-property.js"(exports, module) {
      "use strict";
      require_es_object_define_property();
      var path = require_path();
      var Object2 = path.Object;
      var $defineProperty = module.exports = function defineProperty(it, key, desc) {
        return Object2.defineProperty(it, key, desc);
      };
      if (Object2.defineProperty.sham) $defineProperty.sham = true;
    }
  });

  // node_modules/core-js-pure/stable/object/define-property.js
  var require_define_property2 = __commonJS({
    "node_modules/core-js-pure/stable/object/define-property.js"(exports, module) {
      "use strict";
      var parent = require_define_property();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/object/define-property.js
  var require_define_property3 = __commonJS({
    "node_modules/core-js-pure/actual/object/define-property.js"(exports, module) {
      "use strict";
      var parent = require_define_property2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/object/define-property.js
  var require_define_property4 = __commonJS({
    "node_modules/core-js-pure/full/object/define-property.js"(exports, module) {
      "use strict";
      var parent = require_define_property3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/object/define-property.js
  var require_define_property5 = __commonJS({
    "node_modules/core-js-pure/features/object/define-property.js"(exports, module) {
      "use strict";
      module.exports = require_define_property4();
    }
  });

  // node_modules/@babel/runtime-corejs3/helpers/regeneratorDefine.js
  var require_regeneratorDefine = __commonJS({
    "node_modules/@babel/runtime-corejs3/helpers/regeneratorDefine.js"(exports, module) {
      var _Object$defineProperty4 = require_define_property5();
      function _regeneratorDefine(e, r, n, t) {
        var i = _Object$defineProperty4;
        try {
          i({}, "", {});
        } catch (e2) {
          i = 0;
        }
        module.exports = _regeneratorDefine = function regeneratorDefine(e2, r2, n2, t2) {
          function o(r3, n3) {
            _regeneratorDefine(e2, r3, function(e3) {
              return this._invoke(r3, n3, e3);
            });
          }
          r2 ? i ? i(e2, r2, {
            value: n2,
            enumerable: !t2,
            configurable: !t2,
            writable: !t2
          }) : e2[r2] = n2 : (o("next", 0), o("throw", 1), o("return", 2));
        }, module.exports.__esModule = true, module.exports["default"] = module.exports, _regeneratorDefine(e, r, n, t);
      }
      module.exports = _regeneratorDefine, module.exports.__esModule = true, module.exports["default"] = module.exports;
    }
  });

  // node_modules/@babel/runtime-corejs3/helpers/regenerator.js
  var require_regenerator = __commonJS({
    "node_modules/@babel/runtime-corejs3/helpers/regenerator.js"(exports, module) {
      var _Symbol8 = require_symbol5();
      var _Object$create3 = require_create5();
      var _bindInstanceProperty4 = require_bind6();
      var _Object$getPrototypeOf2 = require_get_prototype_of5();
      var _Object$setPrototypeOf3 = require_set_prototype_of5();
      var regeneratorDefine = require_regeneratorDefine();
      function _regenerator() {
        var e, t, r = "function" == typeof _Symbol8 ? _Symbol8 : {}, n = r.iterator || "@@iterator", o = r.toStringTag || "@@toStringTag";
        function i(r2, n2, o2, i2) {
          var c2 = n2 && n2.prototype instanceof Generator ? n2 : Generator, u2 = _Object$create3(c2.prototype);
          return regeneratorDefine(u2, "_invoke", (function(r3, n3, o3) {
            var i3, c3, u3, f2 = 0, p = o3 || [], y = false, G = {
              p: 0,
              n: 0,
              v: e,
              a: d,
              f: _bindInstanceProperty4(d).call(d, e, 4),
              d: function d2(t2, r4) {
                return i3 = t2, c3 = 0, u3 = e, G.n = r4, a;
              }
            };
            function d(r4, n4) {
              for (c3 = r4, u3 = n4, t = 0; !y && f2 && !o4 && t < p.length; t++) {
                var o4, i4 = p[t], d2 = G.p, l = i4[2];
                r4 > 3 ? (o4 = l === n4) && (u3 = i4[(c3 = i4[4]) ? 5 : (c3 = 3, 3)], i4[4] = i4[5] = e) : i4[0] <= d2 && ((o4 = r4 < 2 && d2 < i4[1]) ? (c3 = 0, G.v = n4, G.n = i4[1]) : d2 < l && (o4 = r4 < 3 || i4[0] > n4 || n4 > l) && (i4[4] = r4, i4[5] = n4, G.n = l, c3 = 0));
              }
              if (o4 || r4 > 1) return a;
              throw y = true, n4;
            }
            return function(o4, p2, l) {
              if (f2 > 1) throw TypeError("Generator is already running");
              for (y && 1 === p2 && d(p2, l), c3 = p2, u3 = l; (t = c3 < 2 ? e : u3) || !y; ) {
                i3 || (c3 ? c3 < 3 ? (c3 > 1 && (G.n = -1), d(c3, u3)) : G.n = u3 : G.v = u3);
                try {
                  if (f2 = 2, i3) {
                    if (c3 || (o4 = "next"), t = i3[o4]) {
                      if (!(t = t.call(i3, u3))) throw TypeError("iterator result is not an object");
                      if (!t.done) return t;
                      u3 = t.value, c3 < 2 && (c3 = 0);
                    } else 1 === c3 && (t = i3["return"]) && t.call(i3), c3 < 2 && (u3 = TypeError("The iterator does not provide a '" + o4 + "' method"), c3 = 1);
                    i3 = e;
                  } else if ((t = (y = G.n < 0) ? u3 : r3.call(n3, G)) !== a) break;
                } catch (t2) {
                  i3 = e, c3 = 1, u3 = t2;
                } finally {
                  f2 = 1;
                }
              }
              return {
                value: t,
                done: y
              };
            };
          })(r2, o2, i2), true), u2;
        }
        var a = {};
        function Generator() {
        }
        function GeneratorFunction() {
        }
        function GeneratorFunctionPrototype() {
        }
        t = _Object$getPrototypeOf2;
        var c = [][n] ? t(t([][n]())) : (regeneratorDefine(t = {}, n, function() {
          return this;
        }), t), u = GeneratorFunctionPrototype.prototype = Generator.prototype = _Object$create3(c);
        function f(e2) {
          return _Object$setPrototypeOf3 ? _Object$setPrototypeOf3(e2, GeneratorFunctionPrototype) : (e2.__proto__ = GeneratorFunctionPrototype, regeneratorDefine(e2, o, "GeneratorFunction")), e2.prototype = _Object$create3(u), e2;
        }
        return GeneratorFunction.prototype = GeneratorFunctionPrototype, regeneratorDefine(u, "constructor", GeneratorFunctionPrototype), regeneratorDefine(GeneratorFunctionPrototype, "constructor", GeneratorFunction), GeneratorFunction.displayName = "GeneratorFunction", regeneratorDefine(GeneratorFunctionPrototype, o, "GeneratorFunction"), regeneratorDefine(u), regeneratorDefine(u, o, "Generator"), regeneratorDefine(u, n, function() {
          return this;
        }), regeneratorDefine(u, "toString", function() {
          return "[object Generator]";
        }), (module.exports = _regenerator = function _regenerator2() {
          return {
            w: i,
            m: f
          };
        }, module.exports.__esModule = true, module.exports["default"] = module.exports)();
      }
      module.exports = _regenerator, module.exports.__esModule = true, module.exports["default"] = module.exports;
    }
  });

  // node_modules/@babel/runtime-corejs3/helpers/regeneratorAsyncIterator.js
  var require_regeneratorAsyncIterator = __commonJS({
    "node_modules/@babel/runtime-corejs3/helpers/regeneratorAsyncIterator.js"(exports, module) {
      var _Symbol8 = require_symbol5();
      var _Symbol$asyncIterator3 = require_async_iterator5();
      var OverloadYield = require_OverloadYield();
      var regeneratorDefine = require_regeneratorDefine();
      function AsyncIterator(t, e) {
        function n(r2, o, i, f) {
          try {
            var c = t[r2](o), u = c.value;
            return u instanceof OverloadYield ? e.resolve(u.v).then(function(t2) {
              n("next", t2, i, f);
            }, function(t2) {
              n("throw", t2, i, f);
            }) : e.resolve(u).then(function(t2) {
              c.value = t2, i(c);
            }, function(t2) {
              return n("throw", t2, i, f);
            });
          } catch (t2) {
            f(t2);
          }
        }
        var r;
        this.next || (regeneratorDefine(AsyncIterator.prototype), regeneratorDefine(AsyncIterator.prototype, "function" == typeof _Symbol8 && _Symbol$asyncIterator3 || "@asyncIterator", function() {
          return this;
        })), regeneratorDefine(this, "_invoke", function(t2, o, i) {
          function f() {
            return new e(function(e2, r2) {
              n(t2, i, e2, r2);
            });
          }
          return r = r ? r.then(f, f) : f();
        }, true);
      }
      module.exports = AsyncIterator, module.exports.__esModule = true, module.exports["default"] = module.exports;
    }
  });

  // node_modules/@babel/runtime-corejs3/helpers/regeneratorAsyncGen.js
  var require_regeneratorAsyncGen = __commonJS({
    "node_modules/@babel/runtime-corejs3/helpers/regeneratorAsyncGen.js"(exports, module) {
      var _Promise5 = require_promise5();
      var regenerator = require_regenerator();
      var regeneratorAsyncIterator = require_regeneratorAsyncIterator();
      function _regeneratorAsyncGen(r, e, t, o, n) {
        return new regeneratorAsyncIterator(regenerator().w(r, e, t, o), n || _Promise5);
      }
      module.exports = _regeneratorAsyncGen, module.exports.__esModule = true, module.exports["default"] = module.exports;
    }
  });

  // node_modules/@babel/runtime-corejs3/helpers/regeneratorAsync.js
  var require_regeneratorAsync = __commonJS({
    "node_modules/@babel/runtime-corejs3/helpers/regeneratorAsync.js"(exports, module) {
      var regeneratorAsyncGen = require_regeneratorAsyncGen();
      function _regeneratorAsync(n, e, r, t, o) {
        var a = regeneratorAsyncGen(n, e, r, t, o);
        return a.next().then(function(n2) {
          return n2.done ? n2.value : a.next();
        });
      }
      module.exports = _regeneratorAsync, module.exports.__esModule = true, module.exports["default"] = module.exports;
    }
  });

  // node_modules/core-js-pure/internals/delete-property-or-throw.js
  var require_delete_property_or_throw = __commonJS({
    "node_modules/core-js-pure/internals/delete-property-or-throw.js"(exports, module) {
      "use strict";
      var tryToString = require_try_to_string();
      var $TypeError = TypeError;
      module.exports = function(O, P) {
        if (!delete O[P]) throw new $TypeError("Cannot delete property " + tryToString(P) + " of " + tryToString(O));
      };
    }
  });

  // node_modules/core-js-pure/modules/es.array.unshift.js
  var require_es_array_unshift = __commonJS({
    "node_modules/core-js-pure/modules/es.array.unshift.js"() {
      "use strict";
      var $ = require_export();
      var toObject = require_to_object();
      var lengthOfArrayLike = require_length_of_array_like();
      var setArrayLength = require_array_set_length();
      var deletePropertyOrThrow = require_delete_property_or_throw();
      var doesNotExceedSafeInteger = require_does_not_exceed_safe_integer();
      var INCORRECT_RESULT = [].unshift(0) !== 1;
      var properErrorOnNonWritableLength = function() {
        try {
          Object.defineProperty([], "length", { writable: false }).unshift();
        } catch (error) {
          return error instanceof TypeError;
        }
      };
      var FORCED = INCORRECT_RESULT || !properErrorOnNonWritableLength();
      $({ target: "Array", proto: true, arity: 1, forced: FORCED }, {
        // eslint-disable-next-line no-unused-vars -- required for `.length`
        unshift: function unshift(item) {
          var O = toObject(this);
          var len = lengthOfArrayLike(O);
          var argCount = arguments.length;
          if (argCount) {
            doesNotExceedSafeInteger(len + argCount);
            var k = len;
            while (k--) {
              var to = k + argCount;
              if (k in O) O[to] = O[k];
              else deletePropertyOrThrow(O, to);
            }
            for (var j = 0; j < argCount; j++) {
              O[j] = arguments[j];
            }
          }
          return setArrayLength(O, len + argCount);
        }
      });
    }
  });

  // node_modules/core-js-pure/es/array/virtual/unshift.js
  var require_unshift = __commonJS({
    "node_modules/core-js-pure/es/array/virtual/unshift.js"(exports, module) {
      "use strict";
      require_es_array_unshift();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Array", "unshift");
    }
  });

  // node_modules/core-js-pure/es/instance/unshift.js
  var require_unshift2 = __commonJS({
    "node_modules/core-js-pure/es/instance/unshift.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_unshift();
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        var own = it.unshift;
        return it === ArrayPrototype || isPrototypeOf(ArrayPrototype, it) && own === ArrayPrototype.unshift ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/unshift.js
  var require_unshift3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/unshift.js"(exports, module) {
      "use strict";
      var parent = require_unshift2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/unshift.js
  var require_unshift4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/unshift.js"(exports, module) {
      "use strict";
      var parent = require_unshift3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/unshift.js
  var require_unshift5 = __commonJS({
    "node_modules/core-js-pure/full/instance/unshift.js"(exports, module) {
      "use strict";
      var parent = require_unshift4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/unshift.js
  var require_unshift6 = __commonJS({
    "node_modules/core-js-pure/features/instance/unshift.js"(exports, module) {
      "use strict";
      module.exports = require_unshift5();
    }
  });

  // node_modules/@babel/runtime-corejs3/helpers/regeneratorKeys.js
  var require_regeneratorKeys = __commonJS({
    "node_modules/@babel/runtime-corejs3/helpers/regeneratorKeys.js"(exports, module) {
      var _unshiftInstanceProperty = require_unshift6();
      function _regeneratorKeys(e) {
        var n = Object(e), r = [];
        for (var t in n) _unshiftInstanceProperty(r).call(r, t);
        return function e2() {
          for (; r.length; ) if ((t = r.pop()) in n) return e2.value = t, e2.done = false, e2;
          return e2.done = true, e2;
        };
      }
      module.exports = _regeneratorKeys, module.exports.__esModule = true, module.exports["default"] = module.exports;
    }
  });

  // node_modules/core-js-pure/es/symbol/iterator.js
  var require_iterator = __commonJS({
    "node_modules/core-js-pure/es/symbol/iterator.js"(exports, module) {
      "use strict";
      require_es_array_iterator();
      require_es_object_to_string();
      require_es_string_iterator();
      require_es_symbol_iterator();
      var WrappedWellKnownSymbolModule = require_well_known_symbol_wrapped();
      module.exports = WrappedWellKnownSymbolModule.f("iterator");
    }
  });

  // node_modules/core-js-pure/stable/symbol/iterator.js
  var require_iterator2 = __commonJS({
    "node_modules/core-js-pure/stable/symbol/iterator.js"(exports, module) {
      "use strict";
      var parent = require_iterator();
      require_web_dom_collections_iterator();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/symbol/iterator.js
  var require_iterator3 = __commonJS({
    "node_modules/core-js-pure/actual/symbol/iterator.js"(exports, module) {
      "use strict";
      var parent = require_iterator2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/symbol/iterator.js
  var require_iterator4 = __commonJS({
    "node_modules/core-js-pure/full/symbol/iterator.js"(exports, module) {
      "use strict";
      var parent = require_iterator3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/symbol/iterator.js
  var require_iterator5 = __commonJS({
    "node_modules/core-js-pure/features/symbol/iterator.js"(exports, module) {
      "use strict";
      module.exports = require_iterator4();
    }
  });

  // node_modules/@babel/runtime-corejs3/helpers/typeof.js
  var require_typeof = __commonJS({
    "node_modules/@babel/runtime-corejs3/helpers/typeof.js"(exports, module) {
      var _Symbol8 = require_symbol5();
      var _Symbol$iterator3 = require_iterator5();
      function _typeof2(o) {
        "@babel/helpers - typeof";
        return module.exports = _typeof2 = "function" == typeof _Symbol8 && "symbol" == typeof _Symbol$iterator3 ? function(o2) {
          return typeof o2;
        } : function(o2) {
          return o2 && "function" == typeof _Symbol8 && o2.constructor === _Symbol8 && o2 !== _Symbol8.prototype ? "symbol" : typeof o2;
        }, module.exports.__esModule = true, module.exports["default"] = module.exports, _typeof2(o);
      }
      module.exports = _typeof2, module.exports.__esModule = true, module.exports["default"] = module.exports;
    }
  });

  // node_modules/@babel/runtime-corejs3/helpers/regeneratorValues.js
  var require_regeneratorValues = __commonJS({
    "node_modules/@babel/runtime-corejs3/helpers/regeneratorValues.js"(exports, module) {
      var _typeof2 = require_typeof()["default"];
      var _Symbol8 = require_symbol5();
      var _Symbol$iterator3 = require_iterator5();
      function _regeneratorValues(e) {
        if (null != e) {
          var t = e["function" == typeof _Symbol8 && _Symbol$iterator3 || "@@iterator"], r = 0;
          if (t) return t.call(e);
          if ("function" == typeof e.next) return e;
          if (!isNaN(e.length)) return {
            next: function next() {
              return e && r >= e.length && (e = void 0), {
                value: e && e[r++],
                done: !e
              };
            }
          };
        }
        throw new TypeError(_typeof2(e) + " is not iterable");
      }
      module.exports = _regeneratorValues, module.exports.__esModule = true, module.exports["default"] = module.exports;
    }
  });

  // node_modules/@babel/runtime-corejs3/helpers/regeneratorRuntime.js
  var require_regeneratorRuntime = __commonJS({
    "node_modules/@babel/runtime-corejs3/helpers/regeneratorRuntime.js"(exports, module) {
      var _Object$getPrototypeOf2 = require_get_prototype_of5();
      var _reverseInstanceProperty = require_reverse6();
      var OverloadYield = require_OverloadYield();
      var regenerator = require_regenerator();
      var regeneratorAsync = require_regeneratorAsync();
      var regeneratorAsyncGen = require_regeneratorAsyncGen();
      var regeneratorAsyncIterator = require_regeneratorAsyncIterator();
      var regeneratorKeys = require_regeneratorKeys();
      var regeneratorValues = require_regeneratorValues();
      function _regeneratorRuntime13() {
        "use strict";
        var r = regenerator(), e = r.m(_regeneratorRuntime13), t = (_Object$getPrototypeOf2 ? _Object$getPrototypeOf2(e) : e.__proto__).constructor;
        function n(r2) {
          var e2 = "function" == typeof r2 && r2.constructor;
          return !!e2 && (e2 === t || "GeneratorFunction" === (e2.displayName || e2.name));
        }
        var o = {
          "throw": 1,
          "return": 2,
          "break": 3,
          "continue": 3
        };
        function a(r2) {
          var e2, t2;
          return function(n2) {
            e2 || (e2 = {
              stop: function stop() {
                return t2(n2.a, 2);
              },
              "catch": function _catch() {
                return n2.v;
              },
              abrupt: function abrupt(r3, e3) {
                return t2(n2.a, o[r3], e3);
              },
              delegateYield: function delegateYield(r3, o2, a2) {
                return e2.resultName = o2, t2(n2.d, regeneratorValues(r3), a2);
              },
              finish: function finish(r3) {
                return t2(n2.f, r3);
              }
            }, t2 = function t3(r3, _t, o2) {
              n2.p = e2.prev, n2.n = e2.next;
              try {
                return r3(_t, o2);
              } finally {
                e2.next = n2.n;
              }
            }), e2.resultName && (e2[e2.resultName] = n2.v, e2.resultName = void 0), e2.sent = n2.v, e2.next = n2.n;
            try {
              return r2.call(this, e2);
            } finally {
              n2.p = e2.prev, n2.n = e2.next;
            }
          };
        }
        return (module.exports = _regeneratorRuntime13 = function _regeneratorRuntime14() {
          return {
            wrap: function wrap(e2, t2, n2, o2) {
              return r.w(a(e2), t2, n2, o2 && _reverseInstanceProperty(o2).call(o2));
            },
            isGeneratorFunction: n,
            mark: r.m,
            awrap: function awrap(r2, e2) {
              return new OverloadYield(r2, e2);
            },
            AsyncIterator: regeneratorAsyncIterator,
            async: function async(r2, e2, t2, o2, u) {
              return (n(e2) ? regeneratorAsyncGen : regeneratorAsync)(a(r2), e2, t2, o2, u);
            },
            keys: regeneratorKeys,
            values: regeneratorValues
          };
        }, module.exports.__esModule = true, module.exports["default"] = module.exports)();
      }
      module.exports = _regeneratorRuntime13, module.exports.__esModule = true, module.exports["default"] = module.exports;
    }
  });

  // node_modules/@babel/runtime-corejs3/regenerator/index.js
  var require_regenerator2 = __commonJS({
    "node_modules/@babel/runtime-corejs3/regenerator/index.js"(exports, module) {
      var runtime = require_regeneratorRuntime()();
      module.exports = runtime;
      try {
        regeneratorRuntime = runtime;
      } catch (accidentalStrictMode) {
        if (typeof globalThis === "object") {
          globalThis.regeneratorRuntime = runtime;
        } else {
          Function("r", "regeneratorRuntime = r")(runtime);
        }
      }
    }
  });

  // node_modules/optimal-select/lib/adapt.js
  var require_adapt = __commonJS({
    "node_modules/optimal-select/lib/adapt.js"(exports, module) {
      "use strict";
      Object.defineProperty(exports, "__esModule", {
        value: true
      });
      var _typeof2 = typeof Symbol === "function" && typeof Symbol.iterator === "symbol" ? function(obj) {
        return typeof obj;
      } : function(obj) {
        return obj && typeof Symbol === "function" && obj.constructor === Symbol && obj !== Symbol.prototype ? "symbol" : typeof obj;
      };
      var _slicedToArray2 = /* @__PURE__ */ (function() {
        function sliceIterator(arr, i) {
          var _arr = [];
          var _n = true;
          var _d = false;
          var _e = void 0;
          try {
            for (var _i = arr[Symbol.iterator](), _s; !(_n = (_s = _i.next()).done); _n = true) {
              _arr.push(_s.value);
              if (i && _arr.length === i) break;
            }
          } catch (err) {
            _d = true;
            _e = err;
          } finally {
            try {
              if (!_n && _i["return"]) _i["return"]();
            } finally {
              if (_d) throw _e;
            }
          }
          return _arr;
        }
        return function(arr, i) {
          if (Array.isArray(arr)) {
            return arr;
          } else if (Symbol.iterator in Object(arr)) {
            return sliceIterator(arr, i);
          } else {
            throw new TypeError("Invalid attempt to destructure non-iterable instance");
          }
        };
      })();
      exports.default = adapt;
      function adapt(element, options) {
        if (global.document) {
          return false;
        } else {
          global.document = options.context || (function() {
            var root = element;
            while (root.parent) {
              root = root.parent;
            }
            return root;
          })();
        }
        var ElementPrototype = Object.getPrototypeOf(global.document);
        if (!Object.getOwnPropertyDescriptor(ElementPrototype, "childTags")) {
          Object.defineProperty(ElementPrototype, "childTags", {
            enumerable: true,
            get: function get() {
              return this.children.filter(function(node) {
                return node.type === "tag" || node.type === "script" || node.type === "style";
              });
            }
          });
        }
        if (!Object.getOwnPropertyDescriptor(ElementPrototype, "attributes")) {
          Object.defineProperty(ElementPrototype, "attributes", {
            enumerable: true,
            get: function get() {
              var attribs = this.attribs;
              var attributesNames = Object.keys(attribs);
              var NamedNodeMap = attributesNames.reduce(function(attributes, attributeName, index) {
                attributes[index] = {
                  name: attributeName,
                  value: attribs[attributeName]
                };
                return attributes;
              }, {});
              Object.defineProperty(NamedNodeMap, "length", {
                enumerable: false,
                configurable: false,
                value: attributesNames.length
              });
              return NamedNodeMap;
            }
          });
        }
        if (!ElementPrototype.getAttribute) {
          ElementPrototype.getAttribute = function(name) {
            return this.attribs[name] || null;
          };
        }
        if (!ElementPrototype.getElementsByTagName) {
          ElementPrototype.getElementsByTagName = function(tagName) {
            var HTMLCollection = [];
            traverseDescendants(this.childTags, function(descendant) {
              if (descendant.name === tagName || tagName === "*") {
                HTMLCollection.push(descendant);
              }
            });
            return HTMLCollection;
          };
        }
        if (!ElementPrototype.getElementsByClassName) {
          ElementPrototype.getElementsByClassName = function(className) {
            var names = className.trim().replace(/\s+/g, " ").split(" ");
            var HTMLCollection = [];
            traverseDescendants([this], function(descendant) {
              var descendantClassName = descendant.attribs.class;
              if (descendantClassName && names.every(function(name) {
                return descendantClassName.indexOf(name) > -1;
              })) {
                HTMLCollection.push(descendant);
              }
            });
            return HTMLCollection;
          };
        }
        if (!ElementPrototype.querySelectorAll) {
          ElementPrototype.querySelectorAll = function(selectors) {
            var _this = this;
            selectors = selectors.replace(/(>)(\S)/g, "$1 $2").trim();
            var instructions = getInstructions(selectors);
            var discover = instructions.shift();
            var total = instructions.length;
            return discover(this).filter(function(node) {
              var step = 0;
              while (step < total) {
                node = instructions[step](node, _this);
                if (!node) {
                  return false;
                }
                step += 1;
              }
              return true;
            });
          };
        }
        if (!ElementPrototype.contains) {
          ElementPrototype.contains = function(element2) {
            var inclusive = false;
            traverseDescendants([this], function(descendant, done) {
              if (descendant === element2) {
                inclusive = true;
                done();
              }
            });
            return inclusive;
          };
        }
        return true;
      }
      function getInstructions(selectors) {
        return selectors.split(" ").reverse().map(function(selector, step) {
          var discover = step === 0;
          var _selector$split = selector.split(":"), _selector$split2 = _slicedToArray2(_selector$split, 2), type = _selector$split2[0], pseudo = _selector$split2[1];
          var validate = null;
          var instruction = null;
          (function() {
            switch (true) {
              // child: '>'
              case />/.test(type):
                instruction = function checkParent(node) {
                  return function(validate2) {
                    return validate2(node.parent) && node.parent;
                  };
                };
                break;
              // class: '.'
              case /^\./.test(type):
                var names = type.substr(1).split(".");
                validate = function validate2(node) {
                  var nodeClassName = node.attribs.class;
                  return nodeClassName && names.every(function(name) {
                    return nodeClassName.indexOf(name) > -1;
                  });
                };
                instruction = function checkClass(node, root) {
                  if (discover) {
                    return node.getElementsByClassName(names.join(" "));
                  }
                  return typeof node === "function" ? node(validate) : getAncestor(node, root, validate);
                };
                break;
              // attribute: '[key="value"]'
              case /^\[/.test(type):
                var _type$replace$split = type.replace(/\[|\]|"/g, "").split("="), _type$replace$split2 = _slicedToArray2(_type$replace$split, 2), attributeKey = _type$replace$split2[0], attributeValue = _type$replace$split2[1];
                validate = function validate2(node) {
                  var hasAttribute = Object.keys(node.attribs).indexOf(attributeKey) > -1;
                  if (hasAttribute) {
                    if (!attributeValue || node.attribs[attributeKey] === attributeValue) {
                      return true;
                    }
                  }
                  return false;
                };
                instruction = function checkAttribute(node, root) {
                  if (discover) {
                    var _ret2 = (function() {
                      var NodeList = [];
                      traverseDescendants([node], function(descendant) {
                        if (validate(descendant)) {
                          NodeList.push(descendant);
                        }
                      });
                      return {
                        v: NodeList
                      };
                    })();
                    if ((typeof _ret2 === "undefined" ? "undefined" : _typeof2(_ret2)) === "object") return _ret2.v;
                  }
                  return typeof node === "function" ? node(validate) : getAncestor(node, root, validate);
                };
                break;
              // id: '#'
              case /^#/.test(type):
                var id = type.substr(1);
                validate = function validate2(node) {
                  return node.attribs.id === id;
                };
                instruction = function checkId(node, root) {
                  if (discover) {
                    var _ret3 = (function() {
                      var NodeList = [];
                      traverseDescendants([node], function(descendant, done) {
                        if (validate(descendant)) {
                          NodeList.push(descendant);
                          done();
                        }
                      });
                      return {
                        v: NodeList
                      };
                    })();
                    if ((typeof _ret3 === "undefined" ? "undefined" : _typeof2(_ret3)) === "object") return _ret3.v;
                  }
                  return typeof node === "function" ? node(validate) : getAncestor(node, root, validate);
                };
                break;
              // universal: '*'
              case /\*/.test(type):
                validate = function validate2(node) {
                  return true;
                };
                instruction = function checkUniversal(node, root) {
                  if (discover) {
                    var _ret4 = (function() {
                      var NodeList = [];
                      traverseDescendants([node], function(descendant) {
                        return NodeList.push(descendant);
                      });
                      return {
                        v: NodeList
                      };
                    })();
                    if ((typeof _ret4 === "undefined" ? "undefined" : _typeof2(_ret4)) === "object") return _ret4.v;
                  }
                  return typeof node === "function" ? node(validate) : getAncestor(node, root, validate);
                };
                break;
              // tag: '...'
              default:
                validate = function validate2(node) {
                  return node.name === type;
                };
                instruction = function checkTag(node, root) {
                  if (discover) {
                    var _ret5 = (function() {
                      var NodeList = [];
                      traverseDescendants([node], function(descendant) {
                        if (validate(descendant)) {
                          NodeList.push(descendant);
                        }
                      });
                      return {
                        v: NodeList
                      };
                    })();
                    if ((typeof _ret5 === "undefined" ? "undefined" : _typeof2(_ret5)) === "object") return _ret5.v;
                  }
                  return typeof node === "function" ? node(validate) : getAncestor(node, root, validate);
                };
            }
          })();
          if (!pseudo) {
            return instruction;
          }
          var rule = pseudo.match(/-(child|type)\((\d+)\)$/);
          var kind = rule[1];
          var index = parseInt(rule[2], 10) - 1;
          var validatePseudo = function validatePseudo2(node) {
            if (node) {
              var compareSet = node.parent.childTags;
              if (kind === "type") {
                compareSet = compareSet.filter(validate);
              }
              var nodeIndex = compareSet.findIndex(function(child) {
                return child === node;
              });
              if (nodeIndex === index) {
                return true;
              }
            }
            return false;
          };
          return function enhanceInstruction(node) {
            var match = instruction(node);
            if (discover) {
              return match.reduce(function(NodeList, matchedNode) {
                if (validatePseudo(matchedNode)) {
                  NodeList.push(matchedNode);
                }
                return NodeList;
              }, []);
            }
            return validatePseudo(match) && match;
          };
        });
      }
      function traverseDescendants(nodes, handler) {
        nodes.forEach(function(node) {
          var progress = true;
          handler(node, function() {
            return progress = false;
          });
          if (node.childTags && progress) {
            traverseDescendants(node.childTags, handler);
          }
        });
      }
      function getAncestor(node, root, validate) {
        while (node.parent) {
          node = node.parent;
          if (validate(node)) {
            return node;
          }
          if (node === root) {
            break;
          }
        }
        return null;
      }
      module.exports = exports["default"];
    }
  });

  // node_modules/optimal-select/lib/utilities.js
  var require_utilities = __commonJS({
    "node_modules/optimal-select/lib/utilities.js"(exports) {
      "use strict";
      Object.defineProperty(exports, "__esModule", {
        value: true
      });
      exports.convertNodeList = convertNodeList;
      exports.escapeValue = escapeValue;
      function convertNodeList(nodes) {
        var length = nodes.length;
        var arr = new Array(length);
        for (var i = 0; i < length; i++) {
          arr[i] = nodes[i];
        }
        return arr;
      }
      function escapeValue(value) {
        return value && value.replace(/['"`\\/:\?&!#$%^()[\]{|}*+;,.<=>@~]/g, "\\$&").replace(/\n/g, "A");
      }
    }
  });

  // node_modules/optimal-select/lib/match.js
  var require_match = __commonJS({
    "node_modules/optimal-select/lib/match.js"(exports, module) {
      "use strict";
      Object.defineProperty(exports, "__esModule", {
        value: true
      });
      exports.default = match;
      var _utilities = require_utilities();
      var defaultIgnore = {
        attribute: function attribute(attributeName) {
          return ["style", "data-reactid", "data-react-checksum"].indexOf(attributeName) > -1;
        }
      };
      function match(node, options) {
        var _options$root = options.root, root = _options$root === void 0 ? document : _options$root, _options$skip = options.skip, skip = _options$skip === void 0 ? null : _options$skip, _options$priority = options.priority, priority = _options$priority === void 0 ? ["id", "class", "href", "src"] : _options$priority, _options$ignore = options.ignore, ignore = _options$ignore === void 0 ? {} : _options$ignore;
        var path = [];
        var element = node;
        var length = path.length;
        var ignoreClass = false;
        var skipCompare = skip && (Array.isArray(skip) ? skip : [skip]).map(function(entry) {
          if (typeof entry !== "function") {
            return function(element2) {
              return element2 === entry;
            };
          }
          return entry;
        });
        var skipChecks = function skipChecks2(element2) {
          return skip && skipCompare.some(function(compare) {
            return compare(element2);
          });
        };
        Object.keys(ignore).forEach(function(type) {
          if (type === "class") {
            ignoreClass = true;
          }
          var predicate = ignore[type];
          if (typeof predicate === "function") return;
          if (typeof predicate === "number") {
            predicate = predicate.toString();
          }
          if (typeof predicate === "string") {
            predicate = new RegExp((0, _utilities.escapeValue)(predicate).replace(/\\/g, "\\\\"));
          }
          if (typeof predicate === "boolean") {
            predicate = predicate ? /(?:)/ : /.^/;
          }
          ignore[type] = function(name, value) {
            return predicate.test(value);
          };
        });
        if (ignoreClass) {
          (function() {
            var ignoreAttribute = ignore.attribute;
            ignore.attribute = function(name, value, defaultPredicate) {
              return ignore.class(value) || ignoreAttribute && ignoreAttribute(name, value, defaultPredicate);
            };
          })();
        }
        while (element !== root) {
          if (skipChecks(element) !== true) {
            if (checkAttributes(priority, element, ignore, path, root)) break;
            if (checkTag(element, ignore, path, root)) break;
            checkAttributes(priority, element, ignore, path);
            if (path.length === length) {
              checkTag(element, ignore, path);
            }
            if (path.length === length) {
              checkChilds(priority, element, ignore, path);
            }
          }
          element = element.parentNode;
          length = path.length;
        }
        if (element === root) {
          var pattern = findPattern(priority, element, ignore);
          path.unshift(pattern);
        }
        return path.join(" ");
      }
      function checkAttributes(priority, element, ignore, path) {
        var parent = arguments.length > 4 && arguments[4] !== void 0 ? arguments[4] : element.parentNode;
        var pattern = findAttributesPattern(priority, element, ignore);
        if (pattern) {
          var matches = parent.querySelectorAll(pattern);
          if (matches.length === 1) {
            path.unshift(pattern);
            return true;
          }
        }
        return false;
      }
      function findAttributesPattern(priority, element, ignore) {
        var attributes = element.attributes;
        var sortedKeys = Object.keys(attributes).sort(function(curr, next) {
          var currPos = priority.indexOf(attributes[curr].name);
          var nextPos = priority.indexOf(attributes[next].name);
          if (nextPos === -1) {
            if (currPos === -1) {
              return 0;
            }
            return -1;
          }
          return currPos - nextPos;
        });
        for (var i = 0, l = sortedKeys.length; i < l; i++) {
          var key = sortedKeys[i];
          var attribute = attributes[key];
          var attributeName = attribute.name;
          var attributeValue = (0, _utilities.escapeValue)(attribute.value);
          var currentIgnore = ignore[attributeName] || ignore.attribute;
          var currentDefaultIgnore = defaultIgnore[attributeName] || defaultIgnore.attribute;
          if (checkIgnore(currentIgnore, attributeName, attributeValue, currentDefaultIgnore)) {
            continue;
          }
          var pattern = "[" + attributeName + '="' + attributeValue + '"]';
          if (/\b\d/.test(attributeValue) === false) {
            if (attributeName === "id") {
              pattern = "#" + attributeValue;
            }
            if (attributeName === "class") {
              var className = attributeValue.trim().replace(/\s+/g, ".");
              pattern = "." + className;
            }
          }
          return pattern;
        }
        return null;
      }
      function checkTag(element, ignore, path) {
        var parent = arguments.length > 3 && arguments[3] !== void 0 ? arguments[3] : element.parentNode;
        var pattern = findTagPattern(element, ignore);
        if (pattern) {
          var matches = parent.getElementsByTagName(pattern);
          if (matches.length === 1) {
            path.unshift(pattern);
            return true;
          }
        }
        return false;
      }
      function findTagPattern(element, ignore) {
        var tagName = element.tagName.toLowerCase();
        if (checkIgnore(ignore.tag, null, tagName)) {
          return null;
        }
        return tagName;
      }
      function checkChilds(priority, element, ignore, path) {
        var parent = element.parentNode;
        var children = parent.childTags || parent.children;
        for (var i = 0, l = children.length; i < l; i++) {
          var child = children[i];
          if (child === element) {
            var childPattern = findPattern(priority, child, ignore);
            if (!childPattern) {
              return console.warn("\n          Element couldn't be matched through strict ignore pattern!\n        ", child, ignore, childPattern);
            }
            var pattern = "> " + childPattern + ":nth-child(" + (i + 1) + ")";
            path.unshift(pattern);
            return true;
          }
        }
        return false;
      }
      function findPattern(priority, element, ignore) {
        var pattern = findAttributesPattern(priority, element, ignore);
        if (!pattern) {
          pattern = findTagPattern(element, ignore);
        }
        return pattern;
      }
      function checkIgnore(predicate, name, value, defaultPredicate) {
        if (!value) {
          return true;
        }
        var check = predicate || defaultPredicate;
        if (!check) {
          return false;
        }
        return check(name, value, defaultPredicate);
      }
      module.exports = exports["default"];
    }
  });

  // node_modules/optimal-select/lib/optimize.js
  var require_optimize = __commonJS({
    "node_modules/optimal-select/lib/optimize.js"(exports, module) {
      "use strict";
      Object.defineProperty(exports, "__esModule", {
        value: true
      });
      exports.default = optimize;
      var _adapt = require_adapt();
      var _adapt2 = _interopRequireDefault(_adapt);
      var _utilities = require_utilities();
      function _interopRequireDefault(obj) {
        return obj && obj.__esModule ? obj : { default: obj };
      }
      function optimize(selector, elements) {
        var options = arguments.length > 2 && arguments[2] !== void 0 ? arguments[2] : {};
        if (!Array.isArray(elements)) {
          elements = !elements.length ? [elements] : (0, _utilities.convertNodeList)(elements);
        }
        if (!elements.length || elements.some(function(element) {
          return element.nodeType !== 1;
        })) {
          throw new Error('Invalid input - to compare HTMLElements its necessary to provide a reference of the selected node(s)! (missing "elements")');
        }
        var globalModified = (0, _adapt2.default)(elements[0], options);
        var path = selector.replace(/> /g, ">").split(/\s+(?=(?:(?:[^"]*"){2})*[^"]*$)/);
        if (path.length < 2) {
          return optimizePart("", selector, "", elements);
        }
        var shortened = [path.pop()];
        while (path.length > 1) {
          var current = path.pop();
          var prePart = path.join(" ");
          var postPart = shortened.join(" ");
          var pattern = prePart + " " + postPart;
          var matches = document.querySelectorAll(pattern);
          if (matches.length !== elements.length) {
            shortened.unshift(optimizePart(prePart, current, postPart, elements));
          }
        }
        shortened.unshift(path[0]);
        path = shortened;
        path[0] = optimizePart("", path[0], path.slice(1).join(" "), elements);
        path[path.length - 1] = optimizePart(path.slice(0, -1).join(" "), path[path.length - 1], "", elements);
        if (globalModified) {
          delete global.document;
        }
        return path.join(" ").replace(/>/g, "> ").trim();
      }
      function optimizePart(prePart, current, postPart, elements) {
        if (prePart.length) prePart = prePart + " ";
        if (postPart.length) postPart = " " + postPart;
        if (/\[*\]/.test(current)) {
          var key = current.replace(/=.*$/, "]");
          var pattern = "" + prePart + key + postPart;
          var matches = document.querySelectorAll(pattern);
          if (compareResults(matches, elements)) {
            current = key;
          } else {
            var references = document.querySelectorAll("" + prePart + key);
            var _loop = function _loop3() {
              var reference = references[i];
              if (elements.some(function(element) {
                return reference.contains(element);
              })) {
                var description = reference.tagName.toLowerCase();
                pattern = "" + prePart + description + postPart;
                matches = document.querySelectorAll(pattern);
                if (compareResults(matches, elements)) {
                  current = description;
                }
                return "break";
              }
            };
            for (var i = 0, l = references.length; i < l; i++) {
              var pattern;
              var matches;
              var _ret = _loop();
              if (_ret === "break") break;
            }
          }
        }
        if (/>/.test(current)) {
          var descendant = current.replace(/>/, "");
          var pattern = "" + prePart + descendant + postPart;
          var matches = document.querySelectorAll(pattern);
          if (compareResults(matches, elements)) {
            current = descendant;
          }
        }
        if (/:nth-child/.test(current)) {
          var type = current.replace(/nth-child/g, "nth-of-type");
          var pattern = "" + prePart + type + postPart;
          var matches = document.querySelectorAll(pattern);
          if (compareResults(matches, elements)) {
            current = type;
          }
        }
        if (/\.\S+\.\S+/.test(current)) {
          var names = current.trim().split(".").slice(1).map(function(name) {
            return "." + name;
          }).sort(function(curr, next) {
            return curr.length - next.length;
          });
          while (names.length) {
            var partial = current.replace(names.shift(), "").trim();
            var pattern = ("" + prePart + partial + postPart).trim();
            if (!pattern.length || pattern.charAt(0) === ">" || pattern.charAt(pattern.length - 1) === ">") {
              break;
            }
            var matches = document.querySelectorAll(pattern);
            if (compareResults(matches, elements)) {
              current = partial;
            }
          }
          names = current && current.match(/\./g);
          if (names && names.length > 2) {
            var _references = document.querySelectorAll("" + prePart + current);
            var _loop2 = function _loop22() {
              var reference = _references[i];
              if (elements.some(function(element) {
                return reference.contains(element);
              })) {
                var description = reference.tagName.toLowerCase();
                pattern = "" + prePart + description + postPart;
                matches = document.querySelectorAll(pattern);
                if (compareResults(matches, elements)) {
                  current = description;
                }
                return "break";
              }
            };
            for (var i = 0, l = _references.length; i < l; i++) {
              var pattern;
              var matches;
              var _ret2 = _loop2();
              if (_ret2 === "break") break;
            }
          }
        }
        return current;
      }
      function compareResults(matches, elements) {
        var length = matches.length;
        return length === elements.length && elements.every(function(element) {
          for (var i = 0; i < length; i++) {
            if (matches[i] === element) {
              return true;
            }
          }
          return false;
        });
      }
      module.exports = exports["default"];
    }
  });

  // node_modules/optimal-select/lib/common.js
  var require_common = __commonJS({
    "node_modules/optimal-select/lib/common.js"(exports) {
      "use strict";
      Object.defineProperty(exports, "__esModule", {
        value: true
      });
      exports.getCommonAncestor = getCommonAncestor;
      exports.getCommonProperties = getCommonProperties;
      function getCommonAncestor(elements) {
        var options = arguments.length > 1 && arguments[1] !== void 0 ? arguments[1] : {};
        var _options$root = options.root, root = _options$root === void 0 ? document : _options$root;
        var ancestors = [];
        elements.forEach(function(element, index) {
          var parents = [];
          while (element !== root) {
            element = element.parentNode;
            parents.unshift(element);
          }
          ancestors[index] = parents;
        });
        ancestors.sort(function(curr, next) {
          return curr.length - next.length;
        });
        var shallowAncestor = ancestors.shift();
        var ancestor = null;
        var _loop = function _loop2() {
          var parent = shallowAncestor[i];
          var missing = ancestors.some(function(otherParents) {
            return !otherParents.some(function(otherParent) {
              return otherParent === parent;
            });
          });
          if (missing) {
            return "break";
          }
          ancestor = parent;
        };
        for (var i = 0, l = shallowAncestor.length; i < l; i++) {
          var _ret = _loop();
          if (_ret === "break") break;
        }
        return ancestor;
      }
      function getCommonProperties(elements) {
        var commonProperties = {
          classes: [],
          attributes: {},
          tag: null
        };
        elements.forEach(function(element) {
          var commonClasses = commonProperties.classes, commonAttributes = commonProperties.attributes, commonTag = commonProperties.tag;
          if (commonClasses !== void 0) {
            var classes = element.getAttribute("class");
            if (classes) {
              classes = classes.trim().split(" ");
              if (!commonClasses.length) {
                commonProperties.classes = classes;
              } else {
                commonClasses = commonClasses.filter(function(entry) {
                  return classes.some(function(name) {
                    return name === entry;
                  });
                });
                if (commonClasses.length) {
                  commonProperties.classes = commonClasses;
                } else {
                  delete commonProperties.classes;
                }
              }
            } else {
              delete commonProperties.classes;
            }
          }
          if (commonAttributes !== void 0) {
            (function() {
              var elementAttributes = element.attributes;
              var attributes = Object.keys(elementAttributes).reduce(function(attributes2, key) {
                var attribute = elementAttributes[key];
                var attributeName = attribute.name;
                if (attribute && attributeName !== "class") {
                  attributes2[attributeName] = attribute.value;
                }
                return attributes2;
              }, {});
              var attributesNames = Object.keys(attributes);
              var commonAttributesNames = Object.keys(commonAttributes);
              if (attributesNames.length) {
                if (!commonAttributesNames.length) {
                  commonProperties.attributes = attributes;
                } else {
                  commonAttributes = commonAttributesNames.reduce(function(nextCommonAttributes, name) {
                    var value = commonAttributes[name];
                    if (value === attributes[name]) {
                      nextCommonAttributes[name] = value;
                    }
                    return nextCommonAttributes;
                  }, {});
                  if (Object.keys(commonAttributes).length) {
                    commonProperties.attributes = commonAttributes;
                  } else {
                    delete commonProperties.attributes;
                  }
                }
              } else {
                delete commonProperties.attributes;
              }
            })();
          }
          if (commonTag !== void 0) {
            var tag = element.tagName.toLowerCase();
            if (!commonTag) {
              commonProperties.tag = tag;
            } else if (tag !== commonTag) {
              delete commonProperties.tag;
            }
          }
        });
        return commonProperties;
      }
    }
  });

  // node_modules/optimal-select/lib/select.js
  var require_select = __commonJS({
    "node_modules/optimal-select/lib/select.js"(exports) {
      "use strict";
      Object.defineProperty(exports, "__esModule", {
        value: true
      });
      var _typeof2 = typeof Symbol === "function" && typeof Symbol.iterator === "symbol" ? function(obj) {
        return typeof obj;
      } : function(obj) {
        return obj && typeof Symbol === "function" && obj.constructor === Symbol && obj !== Symbol.prototype ? "symbol" : typeof obj;
      };
      exports.getSingleSelector = getSingleSelector;
      exports.getMultiSelector = getMultiSelector;
      exports.default = getQuerySelector;
      var _adapt = require_adapt();
      var _adapt2 = _interopRequireDefault(_adapt);
      var _match = require_match();
      var _match2 = _interopRequireDefault(_match);
      var _optimize = require_optimize();
      var _optimize2 = _interopRequireDefault(_optimize);
      var _utilities = require_utilities();
      var _common = require_common();
      function _interopRequireDefault(obj) {
        return obj && obj.__esModule ? obj : { default: obj };
      }
      function getSingleSelector(element) {
        var options = arguments.length > 1 && arguments[1] !== void 0 ? arguments[1] : {};
        if (element.nodeType === 3) {
          element = element.parentNode;
        }
        if (element.nodeType !== 1) {
          throw new Error('Invalid input - only HTMLElements or representations of them are supported! (not "' + (typeof element === "undefined" ? "undefined" : _typeof2(element)) + '")');
        }
        var globalModified = (0, _adapt2.default)(element, options);
        var selector = (0, _match2.default)(element, options);
        var optimized = (0, _optimize2.default)(selector, element, options);
        if (globalModified) {
          delete global.document;
        }
        return optimized;
      }
      function getMultiSelector(elements) {
        var options = arguments.length > 1 && arguments[1] !== void 0 ? arguments[1] : {};
        if (!Array.isArray(elements)) {
          elements = (0, _utilities.convertNodeList)(elements);
        }
        if (elements.some(function(element) {
          return element.nodeType !== 1;
        })) {
          throw new Error("Invalid input - only an Array of HTMLElements or representations of them is supported!");
        }
        var globalModified = (0, _adapt2.default)(elements[0], options);
        var ancestor = (0, _common.getCommonAncestor)(elements, options);
        var ancestorSelector = getSingleSelector(ancestor, options);
        var commonSelectors = getCommonSelectors(elements);
        var descendantSelector = commonSelectors[0];
        var selector = (0, _optimize2.default)(ancestorSelector + " " + descendantSelector, elements, options);
        var selectorMatches = (0, _utilities.convertNodeList)(document.querySelectorAll(selector));
        if (!elements.every(function(element) {
          return selectorMatches.some(function(entry) {
            return entry === element;
          });
        })) {
          return console.warn("\n      The selected elements can't be efficiently mapped.\n      Its probably best to use multiple single selectors instead!\n    ", elements);
        }
        if (globalModified) {
          delete global.document;
        }
        return selector;
      }
      function getCommonSelectors(elements) {
        var _getCommonProperties = (0, _common.getCommonProperties)(elements), classes = _getCommonProperties.classes, attributes = _getCommonProperties.attributes, tag = _getCommonProperties.tag;
        var selectorPath = [];
        if (tag) {
          selectorPath.push(tag);
        }
        if (classes) {
          var classSelector = classes.map(function(name) {
            return "." + name;
          }).join("");
          selectorPath.push(classSelector);
        }
        if (attributes) {
          var attributeSelector = Object.keys(attributes).reduce(function(parts, name) {
            parts.push("[" + name + '="' + attributes[name] + '"]');
            return parts;
          }, []).join("");
          selectorPath.push(attributeSelector);
        }
        if (selectorPath.length) {
        }
        return [selectorPath.join("")];
      }
      function getQuerySelector(input) {
        var options = arguments.length > 1 && arguments[1] !== void 0 ? arguments[1] : {};
        if (input.length && !input.name) {
          return getMultiSelector(input, options);
        }
        return getSingleSelector(input, options);
      }
    }
  });

  // node_modules/optimal-select/lib/index.js
  var require_lib = __commonJS({
    "node_modules/optimal-select/lib/index.js"(exports) {
      "use strict";
      Object.defineProperty(exports, "__esModule", {
        value: true
      });
      exports.default = exports.common = exports.optimize = exports.getMultiSelector = exports.getSingleSelector = exports.select = void 0;
      var _select2 = require_select();
      Object.defineProperty(exports, "getSingleSelector", {
        enumerable: true,
        get: function get() {
          return _select2.getSingleSelector;
        }
      });
      Object.defineProperty(exports, "getMultiSelector", {
        enumerable: true,
        get: function get() {
          return _select2.getMultiSelector;
        }
      });
      var _select3 = _interopRequireDefault(_select2);
      var _optimize2 = require_optimize();
      var _optimize3 = _interopRequireDefault(_optimize2);
      var _common2 = require_common();
      var _common = _interopRequireWildcard(_common2);
      function _interopRequireWildcard(obj) {
        if (obj && obj.__esModule) {
          return obj;
        } else {
          var newObj = {};
          if (obj != null) {
            for (var key in obj) {
              if (Object.prototype.hasOwnProperty.call(obj, key)) newObj[key] = obj[key];
            }
          }
          newObj.default = obj;
          return newObj;
        }
      }
      function _interopRequireDefault(obj) {
        return obj && obj.__esModule ? obj : { default: obj };
      }
      exports.select = _select3.default;
      exports.optimize = _optimize3.default;
      exports.common = _common;
      exports.default = _select3.default;
    }
  });

  // node_modules/core-js-pure/modules/es.array.is-array.js
  var require_es_array_is_array = __commonJS({
    "node_modules/core-js-pure/modules/es.array.is-array.js"() {
      "use strict";
      var $ = require_export();
      var isArray = require_is_array();
      $({ target: "Array", stat: true }, {
        isArray
      });
    }
  });

  // node_modules/core-js-pure/es/array/is-array.js
  var require_is_array2 = __commonJS({
    "node_modules/core-js-pure/es/array/is-array.js"(exports, module) {
      "use strict";
      require_es_array_is_array();
      var path = require_path();
      module.exports = path.Array.isArray;
    }
  });

  // node_modules/core-js-pure/stable/array/is-array.js
  var require_is_array3 = __commonJS({
    "node_modules/core-js-pure/stable/array/is-array.js"(exports, module) {
      "use strict";
      var parent = require_is_array2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/array/is-array.js
  var require_is_array4 = __commonJS({
    "node_modules/core-js-pure/actual/array/is-array.js"(exports, module) {
      "use strict";
      var parent = require_is_array3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/array/is-array.js
  var require_is_array5 = __commonJS({
    "node_modules/core-js-pure/full/array/is-array.js"(exports, module) {
      "use strict";
      var parent = require_is_array4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/array/is-array.js
  var require_is_array6 = __commonJS({
    "node_modules/core-js-pure/features/array/is-array.js"(exports, module) {
      "use strict";
      module.exports = require_is_array5();
    }
  });

  // node_modules/core-js-pure/modules/es.array.push.js
  var require_es_array_push = __commonJS({
    "node_modules/core-js-pure/modules/es.array.push.js"() {
      "use strict";
      var $ = require_export();
      var toObject = require_to_object();
      var lengthOfArrayLike = require_length_of_array_like();
      var setArrayLength = require_array_set_length();
      var doesNotExceedSafeInteger = require_does_not_exceed_safe_integer();
      var fails = require_fails();
      var INCORRECT_TO_LENGTH = fails(function() {
        return [].push.call({ length: 4294967296 }, 1) !== 4294967297;
      });
      var properErrorOnNonWritableLength = function() {
        try {
          Object.defineProperty([], "length", { writable: false }).push();
        } catch (error) {
          return error instanceof TypeError;
        }
      };
      var FORCED = INCORRECT_TO_LENGTH || !properErrorOnNonWritableLength();
      $({ target: "Array", proto: true, arity: 1, forced: FORCED }, {
        // eslint-disable-next-line no-unused-vars -- required for `.length`
        push: function push(item) {
          var O = toObject(this);
          var len = lengthOfArrayLike(O);
          var argCount = arguments.length;
          doesNotExceedSafeInteger(len + argCount);
          for (var i = 0; i < argCount; i++) {
            O[len] = arguments[i];
            len++;
          }
          setArrayLength(O, len);
          return len;
        }
      });
    }
  });

  // node_modules/core-js-pure/es/array/virtual/push.js
  var require_push = __commonJS({
    "node_modules/core-js-pure/es/array/virtual/push.js"(exports, module) {
      "use strict";
      require_es_array_push();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Array", "push");
    }
  });

  // node_modules/core-js-pure/es/instance/push.js
  var require_push2 = __commonJS({
    "node_modules/core-js-pure/es/instance/push.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_push();
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        var own = it.push;
        return it === ArrayPrototype || isPrototypeOf(ArrayPrototype, it) && own === ArrayPrototype.push ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/push.js
  var require_push3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/push.js"(exports, module) {
      "use strict";
      var parent = require_push2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/push.js
  var require_push4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/push.js"(exports, module) {
      "use strict";
      var parent = require_push3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/push.js
  var require_push5 = __commonJS({
    "node_modules/core-js-pure/full/instance/push.js"(exports, module) {
      "use strict";
      var parent = require_push4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/push.js
  var require_push6 = __commonJS({
    "node_modules/core-js-pure/features/instance/push.js"(exports, module) {
      "use strict";
      module.exports = require_push5();
    }
  });

  // node_modules/core-js-pure/modules/es.array.map.js
  var require_es_array_map = __commonJS({
    "node_modules/core-js-pure/modules/es.array.map.js"() {
      "use strict";
      var $ = require_export();
      var $map = require_array_iteration().map;
      var arrayMethodHasSpeciesSupport = require_array_method_has_species_support();
      var HAS_SPECIES_SUPPORT = arrayMethodHasSpeciesSupport("map");
      $({ target: "Array", proto: true, forced: !HAS_SPECIES_SUPPORT }, {
        map: function map(callbackfn) {
          return $map(this, callbackfn, arguments.length > 1 ? arguments[1] : void 0);
        }
      });
    }
  });

  // node_modules/core-js-pure/es/array/virtual/map.js
  var require_map = __commonJS({
    "node_modules/core-js-pure/es/array/virtual/map.js"(exports, module) {
      "use strict";
      require_es_array_map();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Array", "map");
    }
  });

  // node_modules/core-js-pure/es/instance/map.js
  var require_map2 = __commonJS({
    "node_modules/core-js-pure/es/instance/map.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_map();
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        var own = it.map;
        return it === ArrayPrototype || isPrototypeOf(ArrayPrototype, it) && own === ArrayPrototype.map ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/map.js
  var require_map3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/map.js"(exports, module) {
      "use strict";
      var parent = require_map2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/map.js
  var require_map4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/map.js"(exports, module) {
      "use strict";
      var parent = require_map3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/map.js
  var require_map5 = __commonJS({
    "node_modules/core-js-pure/full/instance/map.js"(exports, module) {
      "use strict";
      var parent = require_map4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/map.js
  var require_map6 = __commonJS({
    "node_modules/core-js-pure/features/instance/map.js"(exports, module) {
      "use strict";
      module.exports = require_map5();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/instance/map.js
  var require_map7 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/instance/map.js"(exports, module) {
      module.exports = require_map6();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/promise.js
  var require_promise6 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/promise.js"(exports, module) {
      module.exports = require_promise5();
    }
  });

  // node_modules/core-js-pure/internals/array-reduce.js
  var require_array_reduce = __commonJS({
    "node_modules/core-js-pure/internals/array-reduce.js"(exports, module) {
      "use strict";
      var aCallable = require_a_callable();
      var toObject = require_to_object();
      var IndexedObject = require_indexed_object();
      var lengthOfArrayLike = require_length_of_array_like();
      var $TypeError = TypeError;
      var REDUCE_EMPTY = "Reduce of empty array with no initial value";
      var createMethod = function(IS_RIGHT) {
        return function(that, callbackfn, argumentsLength, memo) {
          var O = toObject(that);
          var self2 = IndexedObject(O);
          var length = lengthOfArrayLike(O);
          aCallable(callbackfn);
          if (length === 0 && argumentsLength < 2) throw new $TypeError(REDUCE_EMPTY);
          var index = IS_RIGHT ? length - 1 : 0;
          var i = IS_RIGHT ? -1 : 1;
          if (argumentsLength < 2) while (true) {
            if (index in self2) {
              memo = self2[index];
              index += i;
              break;
            }
            index += i;
            if (IS_RIGHT ? index < 0 : length <= index) {
              throw new $TypeError(REDUCE_EMPTY);
            }
          }
          for (; IS_RIGHT ? index >= 0 : length > index; index += i) if (index in self2) {
            memo = callbackfn(memo, self2[index], index, O);
          }
          return memo;
        };
      };
      module.exports = {
        // `Array.prototype.reduce` method
        // https://tc39.es/ecma262/#sec-array.prototype.reduce
        left: createMethod(false),
        // `Array.prototype.reduceRight` method
        // https://tc39.es/ecma262/#sec-array.prototype.reduceright
        right: createMethod(true)
      };
    }
  });

  // node_modules/core-js-pure/internals/array-method-is-strict.js
  var require_array_method_is_strict = __commonJS({
    "node_modules/core-js-pure/internals/array-method-is-strict.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      module.exports = function(METHOD_NAME, argument) {
        var method = [][METHOD_NAME];
        return !!method && fails(function() {
          method.call(null, argument || function() {
            return 1;
          }, 1);
        });
      };
    }
  });

  // node_modules/core-js-pure/modules/es.array.reduce.js
  var require_es_array_reduce = __commonJS({
    "node_modules/core-js-pure/modules/es.array.reduce.js"() {
      "use strict";
      var $ = require_export();
      var $reduce = require_array_reduce().left;
      var arrayMethodIsStrict = require_array_method_is_strict();
      var CHROME_VERSION = require_environment_v8_version();
      var IS_NODE = require_environment_is_node();
      var CHROME_BUG = !IS_NODE && CHROME_VERSION > 79 && CHROME_VERSION < 83;
      var FORCED = CHROME_BUG || !arrayMethodIsStrict("reduce");
      $({ target: "Array", proto: true, forced: FORCED }, {
        reduce: function reduce(callbackfn) {
          var length = arguments.length;
          return $reduce(this, callbackfn, length, length > 1 ? arguments[1] : void 0);
        }
      });
    }
  });

  // node_modules/core-js-pure/es/array/virtual/reduce.js
  var require_reduce = __commonJS({
    "node_modules/core-js-pure/es/array/virtual/reduce.js"(exports, module) {
      "use strict";
      require_es_array_reduce();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Array", "reduce");
    }
  });

  // node_modules/core-js-pure/es/instance/reduce.js
  var require_reduce2 = __commonJS({
    "node_modules/core-js-pure/es/instance/reduce.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_reduce();
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        var own = it.reduce;
        return it === ArrayPrototype || isPrototypeOf(ArrayPrototype, it) && own === ArrayPrototype.reduce ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/reduce.js
  var require_reduce3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/reduce.js"(exports, module) {
      "use strict";
      var parent = require_reduce2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/reduce.js
  var require_reduce4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/reduce.js"(exports, module) {
      "use strict";
      var parent = require_reduce3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/reduce.js
  var require_reduce5 = __commonJS({
    "node_modules/core-js-pure/full/instance/reduce.js"(exports, module) {
      "use strict";
      var parent = require_reduce4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/reduce.js
  var require_reduce6 = __commonJS({
    "node_modules/core-js-pure/features/instance/reduce.js"(exports, module) {
      "use strict";
      module.exports = require_reduce5();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/instance/reduce.js
  var require_reduce7 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/instance/reduce.js"(exports, module) {
      module.exports = require_reduce6();
    }
  });

  // node_modules/core-js-pure/internals/flatten-into-array.js
  var require_flatten_into_array = __commonJS({
    "node_modules/core-js-pure/internals/flatten-into-array.js"(exports, module) {
      "use strict";
      var isArray = require_is_array();
      var lengthOfArrayLike = require_length_of_array_like();
      var doesNotExceedSafeInteger = require_does_not_exceed_safe_integer();
      var bind = require_function_bind_context();
      var createProperty = require_create_property();
      var flattenIntoArray = function(target, original, source, sourceLen, start, depth, mapper, thisArg) {
        var targetIndex = start;
        var sourceIndex = 0;
        var mapFn = mapper ? bind(mapper, thisArg) : false;
        var element, elementLen;
        while (sourceIndex < sourceLen) {
          if (sourceIndex in source) {
            element = mapFn ? mapFn(source[sourceIndex], sourceIndex, original) : source[sourceIndex];
            if (depth > 0 && isArray(element)) {
              elementLen = lengthOfArrayLike(element);
              targetIndex = flattenIntoArray(target, original, element, elementLen, targetIndex, depth - 1) - 1;
            } else {
              doesNotExceedSafeInteger(targetIndex + 1);
              createProperty(target, targetIndex, element);
            }
            targetIndex++;
          }
          sourceIndex++;
        }
        return targetIndex;
      };
      module.exports = flattenIntoArray;
    }
  });

  // node_modules/core-js-pure/modules/es.array.flat-map.js
  var require_es_array_flat_map = __commonJS({
    "node_modules/core-js-pure/modules/es.array.flat-map.js"() {
      "use strict";
      var $ = require_export();
      var flattenIntoArray = require_flatten_into_array();
      var aCallable = require_a_callable();
      var toObject = require_to_object();
      var lengthOfArrayLike = require_length_of_array_like();
      var arraySpeciesCreate = require_array_species_create();
      $({ target: "Array", proto: true }, {
        flatMap: function flatMap(callbackfn) {
          var O = toObject(this);
          var sourceLen = lengthOfArrayLike(O);
          var A;
          aCallable(callbackfn);
          A = arraySpeciesCreate(O, 0);
          flattenIntoArray(A, O, O, sourceLen, 0, 1, callbackfn, arguments.length > 1 ? arguments[1] : void 0);
          return A;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.array.unscopables.flat-map.js
  var require_es_array_unscopables_flat_map = __commonJS({
    "node_modules/core-js-pure/modules/es.array.unscopables.flat-map.js"() {
      "use strict";
      var addToUnscopables = require_add_to_unscopables();
      addToUnscopables("flatMap");
    }
  });

  // node_modules/core-js-pure/es/array/virtual/flat-map.js
  var require_flat_map = __commonJS({
    "node_modules/core-js-pure/es/array/virtual/flat-map.js"(exports, module) {
      "use strict";
      require_es_array_flat_map();
      require_es_array_unscopables_flat_map();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Array", "flatMap");
    }
  });

  // node_modules/core-js-pure/es/instance/flat-map.js
  var require_flat_map2 = __commonJS({
    "node_modules/core-js-pure/es/instance/flat-map.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_flat_map();
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        var own = it.flatMap;
        return it === ArrayPrototype || isPrototypeOf(ArrayPrototype, it) && own === ArrayPrototype.flatMap ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/flat-map.js
  var require_flat_map3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/flat-map.js"(exports, module) {
      "use strict";
      var parent = require_flat_map2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/flat-map.js
  var require_flat_map4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/flat-map.js"(exports, module) {
      "use strict";
      var parent = require_flat_map3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/flat-map.js
  var require_flat_map5 = __commonJS({
    "node_modules/core-js-pure/full/instance/flat-map.js"(exports, module) {
      "use strict";
      var parent = require_flat_map4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/flat-map.js
  var require_flat_map6 = __commonJS({
    "node_modules/core-js-pure/features/instance/flat-map.js"(exports, module) {
      "use strict";
      module.exports = require_flat_map5();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/instance/flat-map.js
  var require_flat_map7 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/instance/flat-map.js"(exports, module) {
      module.exports = require_flat_map6();
    }
  });

  // node_modules/core-js-pure/es/array/virtual/concat.js
  var require_concat = __commonJS({
    "node_modules/core-js-pure/es/array/virtual/concat.js"(exports, module) {
      "use strict";
      require_es_array_concat();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Array", "concat");
    }
  });

  // node_modules/core-js-pure/es/instance/concat.js
  var require_concat2 = __commonJS({
    "node_modules/core-js-pure/es/instance/concat.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_concat();
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        var own = it.concat;
        return it === ArrayPrototype || isPrototypeOf(ArrayPrototype, it) && own === ArrayPrototype.concat ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/concat.js
  var require_concat3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/concat.js"(exports, module) {
      "use strict";
      var parent = require_concat2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/concat.js
  var require_concat4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/concat.js"(exports, module) {
      "use strict";
      var parent = require_concat3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/concat.js
  var require_concat5 = __commonJS({
    "node_modules/core-js-pure/full/instance/concat.js"(exports, module) {
      "use strict";
      var parent = require_concat4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/concat.js
  var require_concat6 = __commonJS({
    "node_modules/core-js-pure/features/instance/concat.js"(exports, module) {
      "use strict";
      module.exports = require_concat5();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/instance/concat.js
  var require_concat7 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/instance/concat.js"(exports, module) {
      module.exports = require_concat6();
    }
  });

  // node_modules/core-js-pure/modules/es.date.to-primitive.js
  var require_es_date_to_primitive = __commonJS({
    "node_modules/core-js-pure/modules/es.date.to-primitive.js"() {
    }
  });

  // node_modules/core-js-pure/es/symbol/to-primitive.js
  var require_to_primitive2 = __commonJS({
    "node_modules/core-js-pure/es/symbol/to-primitive.js"(exports, module) {
      "use strict";
      require_es_date_to_primitive();
      require_es_symbol_to_primitive();
      var WrappedWellKnownSymbolModule = require_well_known_symbol_wrapped();
      module.exports = WrappedWellKnownSymbolModule.f("toPrimitive");
    }
  });

  // node_modules/core-js-pure/stable/symbol/to-primitive.js
  var require_to_primitive3 = __commonJS({
    "node_modules/core-js-pure/stable/symbol/to-primitive.js"(exports, module) {
      "use strict";
      var parent = require_to_primitive2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/symbol/to-primitive.js
  var require_to_primitive4 = __commonJS({
    "node_modules/core-js-pure/actual/symbol/to-primitive.js"(exports, module) {
      "use strict";
      var parent = require_to_primitive3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/symbol/to-primitive.js
  var require_to_primitive5 = __commonJS({
    "node_modules/core-js-pure/full/symbol/to-primitive.js"(exports, module) {
      "use strict";
      var parent = require_to_primitive4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/symbol/to-primitive.js
  var require_to_primitive6 = __commonJS({
    "node_modules/core-js-pure/features/symbol/to-primitive.js"(exports, module) {
      "use strict";
      module.exports = require_to_primitive5();
    }
  });

  // node_modules/core-js-pure/internals/is-regexp.js
  var require_is_regexp = __commonJS({
    "node_modules/core-js-pure/internals/is-regexp.js"(exports, module) {
      "use strict";
      var isObject = require_is_object();
      var classof = require_classof_raw();
      var wellKnownSymbol = require_well_known_symbol();
      var MATCH = wellKnownSymbol("match");
      module.exports = function(it) {
        var isRegExp;
        return isObject(it) && ((isRegExp = it[MATCH]) !== void 0 ? !!isRegExp : classof(it) === "RegExp");
      };
    }
  });

  // node_modules/core-js-pure/internals/not-a-regexp.js
  var require_not_a_regexp = __commonJS({
    "node_modules/core-js-pure/internals/not-a-regexp.js"(exports, module) {
      "use strict";
      var isRegExp = require_is_regexp();
      var $TypeError = TypeError;
      module.exports = function(it) {
        if (isRegExp(it)) {
          throw new $TypeError("The method doesn't accept regular expressions");
        }
        return it;
      };
    }
  });

  // node_modules/core-js-pure/internals/correct-is-regexp-logic.js
  var require_correct_is_regexp_logic = __commonJS({
    "node_modules/core-js-pure/internals/correct-is-regexp-logic.js"(exports, module) {
      "use strict";
      var wellKnownSymbol = require_well_known_symbol();
      var MATCH = wellKnownSymbol("match");
      module.exports = function(METHOD_NAME) {
        var regexp = /./;
        try {
          "/./"[METHOD_NAME](regexp);
        } catch (error1) {
          try {
            regexp[MATCH] = false;
            return "/./"[METHOD_NAME](regexp);
          } catch (error2) {
          }
        }
        return false;
      };
    }
  });

  // node_modules/core-js-pure/modules/es.string.starts-with.js
  var require_es_string_starts_with = __commonJS({
    "node_modules/core-js-pure/modules/es.string.starts-with.js"() {
      "use strict";
      var $ = require_export();
      var uncurryThis = require_function_uncurry_this_clause();
      var getOwnPropertyDescriptor = require_object_get_own_property_descriptor().f;
      var toLength = require_to_length();
      var toString = require_to_string();
      var notARegExp = require_not_a_regexp();
      var requireObjectCoercible = require_require_object_coercible();
      var correctIsRegExpLogic = require_correct_is_regexp_logic();
      var IS_PURE = require_is_pure();
      var stringSlice = uncurryThis("".slice);
      var min = Math.min;
      var CORRECT_IS_REGEXP_LOGIC = correctIsRegExpLogic("startsWith");
      var MDN_POLYFILL_BUG = !IS_PURE && !CORRECT_IS_REGEXP_LOGIC && !!(function() {
        var descriptor = getOwnPropertyDescriptor(String.prototype, "startsWith");
        return descriptor && !descriptor.writable;
      })();
      $({ target: "String", proto: true, forced: !MDN_POLYFILL_BUG && !CORRECT_IS_REGEXP_LOGIC }, {
        startsWith: function startsWith(searchString) {
          var that = toString(requireObjectCoercible(this));
          notARegExp(searchString);
          var search = toString(searchString);
          var index = toLength(min(arguments.length > 1 ? arguments[1] : void 0, that.length));
          return stringSlice(that, index, index + search.length) === search;
        }
      });
    }
  });

  // node_modules/core-js-pure/es/string/virtual/starts-with.js
  var require_starts_with = __commonJS({
    "node_modules/core-js-pure/es/string/virtual/starts-with.js"(exports, module) {
      "use strict";
      require_es_string_starts_with();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("String", "startsWith");
    }
  });

  // node_modules/core-js-pure/es/instance/starts-with.js
  var require_starts_with2 = __commonJS({
    "node_modules/core-js-pure/es/instance/starts-with.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_starts_with();
      var StringPrototype = String.prototype;
      module.exports = function(it) {
        var own = it.startsWith;
        return typeof it == "string" || it === StringPrototype || isPrototypeOf(StringPrototype, it) && own === StringPrototype.startsWith ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/starts-with.js
  var require_starts_with3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/starts-with.js"(exports, module) {
      "use strict";
      var parent = require_starts_with2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/starts-with.js
  var require_starts_with4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/starts-with.js"(exports, module) {
      "use strict";
      var parent = require_starts_with3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/starts-with.js
  var require_starts_with5 = __commonJS({
    "node_modules/core-js-pure/full/instance/starts-with.js"(exports, module) {
      "use strict";
      var parent = require_starts_with4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/starts-with.js
  var require_starts_with6 = __commonJS({
    "node_modules/core-js-pure/features/instance/starts-with.js"(exports, module) {
      "use strict";
      module.exports = require_starts_with5();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/instance/starts-with.js
  var require_starts_with7 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/instance/starts-with.js"(exports, module) {
      module.exports = require_starts_with6();
    }
  });

  // node_modules/core-js-pure/modules/es.array.filter.js
  var require_es_array_filter = __commonJS({
    "node_modules/core-js-pure/modules/es.array.filter.js"() {
      "use strict";
      var $ = require_export();
      var $filter = require_array_iteration().filter;
      var arrayMethodHasSpeciesSupport = require_array_method_has_species_support();
      var HAS_SPECIES_SUPPORT = arrayMethodHasSpeciesSupport("filter");
      $({ target: "Array", proto: true, forced: !HAS_SPECIES_SUPPORT }, {
        filter: function filter(callbackfn) {
          return $filter(this, callbackfn, arguments.length > 1 ? arguments[1] : void 0);
        }
      });
    }
  });

  // node_modules/core-js-pure/es/array/virtual/filter.js
  var require_filter = __commonJS({
    "node_modules/core-js-pure/es/array/virtual/filter.js"(exports, module) {
      "use strict";
      require_es_array_filter();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Array", "filter");
    }
  });

  // node_modules/core-js-pure/es/instance/filter.js
  var require_filter2 = __commonJS({
    "node_modules/core-js-pure/es/instance/filter.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_filter();
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        var own = it.filter;
        return it === ArrayPrototype || isPrototypeOf(ArrayPrototype, it) && own === ArrayPrototype.filter ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/filter.js
  var require_filter3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/filter.js"(exports, module) {
      "use strict";
      var parent = require_filter2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/filter.js
  var require_filter4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/filter.js"(exports, module) {
      "use strict";
      var parent = require_filter3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/filter.js
  var require_filter5 = __commonJS({
    "node_modules/core-js-pure/full/instance/filter.js"(exports, module) {
      "use strict";
      var parent = require_filter4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/filter.js
  var require_filter6 = __commonJS({
    "node_modules/core-js-pure/features/instance/filter.js"(exports, module) {
      "use strict";
      module.exports = require_filter5();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/instance/filter.js
  var require_filter7 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/instance/filter.js"(exports, module) {
      module.exports = require_filter6();
    }
  });

  // node_modules/core-js-pure/modules/es.reflect.construct.js
  var require_es_reflect_construct = __commonJS({
    "node_modules/core-js-pure/modules/es.reflect.construct.js"() {
      "use strict";
      var $ = require_export();
      var getBuiltIn = require_get_built_in();
      var apply = require_function_apply();
      var bind = require_function_bind();
      var aConstructor = require_a_constructor();
      var anObject = require_an_object();
      var isObject = require_is_object();
      var create = require_object_create();
      var fails = require_fails();
      var nativeConstruct = getBuiltIn("Reflect", "construct");
      var ObjectPrototype = Object.prototype;
      var push = [].push;
      var NEW_TARGET_BUG = fails(function() {
        function F() {
        }
        return !(nativeConstruct(function() {
        }, [], F) instanceof F);
      });
      var ARGS_BUG = !fails(function() {
        nativeConstruct(function() {
        });
      });
      var FORCED = NEW_TARGET_BUG || ARGS_BUG;
      $({ target: "Reflect", stat: true, forced: FORCED, sham: FORCED }, {
        construct: function construct(Target, args) {
          aConstructor(Target);
          var newTarget = arguments.length < 3 ? Target : aConstructor(arguments[2]);
          anObject(args);
          if (ARGS_BUG && !NEW_TARGET_BUG) return nativeConstruct(Target, args, newTarget);
          if (Target === newTarget) {
            switch (args.length) {
              case 0:
                return new Target();
              case 1:
                return new Target(args[0]);
              case 2:
                return new Target(args[0], args[1]);
              case 3:
                return new Target(args[0], args[1], args[2]);
              case 4:
                return new Target(args[0], args[1], args[2], args[3]);
            }
            var $args = [null];
            apply(push, $args, args);
            return new (apply(bind, Target, $args))();
          }
          var proto = newTarget.prototype;
          var instance = create(isObject(proto) ? proto : ObjectPrototype);
          var result = apply(Target, instance, args);
          return isObject(result) ? result : instance;
        }
      });
    }
  });

  // node_modules/core-js-pure/es/reflect/construct.js
  var require_construct = __commonJS({
    "node_modules/core-js-pure/es/reflect/construct.js"(exports, module) {
      "use strict";
      require_es_reflect_construct();
      var path = require_path();
      module.exports = path.Reflect.construct;
    }
  });

  // node_modules/core-js-pure/stable/reflect/construct.js
  var require_construct2 = __commonJS({
    "node_modules/core-js-pure/stable/reflect/construct.js"(exports, module) {
      "use strict";
      var parent = require_construct();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/reflect/construct.js
  var require_construct3 = __commonJS({
    "node_modules/core-js-pure/actual/reflect/construct.js"(exports, module) {
      "use strict";
      var parent = require_construct2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/reflect/construct.js
  var require_construct4 = __commonJS({
    "node_modules/core-js-pure/full/reflect/construct.js"(exports, module) {
      "use strict";
      var parent = require_construct3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/reflect/construct.js
  var require_construct5 = __commonJS({
    "node_modules/core-js-pure/features/reflect/construct.js"(exports, module) {
      "use strict";
      module.exports = require_construct4();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/reflect/construct.js
  var require_construct6 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/reflect/construct.js"(exports, module) {
      module.exports = require_construct5();
    }
  });

  // node_modules/core-js-pure/internals/array-buffer-non-extensible.js
  var require_array_buffer_non_extensible = __commonJS({
    "node_modules/core-js-pure/internals/array-buffer-non-extensible.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      module.exports = fails(function() {
        if (typeof ArrayBuffer == "function") {
          var buffer = new ArrayBuffer(8);
          if (Object.isExtensible(buffer)) Object.defineProperty(buffer, "a", { value: 8 });
        }
      });
    }
  });

  // node_modules/core-js-pure/internals/object-is-extensible.js
  var require_object_is_extensible = __commonJS({
    "node_modules/core-js-pure/internals/object-is-extensible.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      var isObject = require_is_object();
      var classof = require_classof_raw();
      var ARRAY_BUFFER_NON_EXTENSIBLE = require_array_buffer_non_extensible();
      var $isExtensible = Object.isExtensible;
      var FAILS_ON_PRIMITIVES = fails(function() {
        $isExtensible(1);
      });
      module.exports = FAILS_ON_PRIMITIVES || ARRAY_BUFFER_NON_EXTENSIBLE ? function isExtensible(it) {
        if (!isObject(it)) return false;
        if (ARRAY_BUFFER_NON_EXTENSIBLE && classof(it) === "ArrayBuffer") return false;
        return $isExtensible ? $isExtensible(it) : true;
      } : $isExtensible;
    }
  });

  // node_modules/core-js-pure/internals/freezing.js
  var require_freezing = __commonJS({
    "node_modules/core-js-pure/internals/freezing.js"(exports, module) {
      "use strict";
      var fails = require_fails();
      module.exports = !fails(function() {
        return Object.isExtensible(Object.preventExtensions({}));
      });
    }
  });

  // node_modules/core-js-pure/internals/internal-metadata.js
  var require_internal_metadata = __commonJS({
    "node_modules/core-js-pure/internals/internal-metadata.js"(exports, module) {
      "use strict";
      var $ = require_export();
      var uncurryThis = require_function_uncurry_this();
      var hiddenKeys = require_hidden_keys();
      var isObject = require_is_object();
      var hasOwn = require_has_own_property();
      var defineProperty = require_object_define_property().f;
      var getOwnPropertyNamesModule = require_object_get_own_property_names();
      var getOwnPropertyNamesExternalModule = require_object_get_own_property_names_external();
      var isExtensible = require_object_is_extensible();
      var uid = require_uid();
      var FREEZING = require_freezing();
      var REQUIRED = false;
      var METADATA = uid("meta");
      var id = 0;
      var setMetadata = function(it) {
        defineProperty(it, METADATA, { value: {
          objectID: "O" + id++,
          // object ID
          weakData: {}
          // weak collections IDs
        } });
      };
      var fastKey = function(it, create) {
        if (!isObject(it)) return typeof it == "symbol" ? it : (typeof it == "string" ? "S" : "P") + it;
        if (!hasOwn(it, METADATA)) {
          if (!isExtensible(it)) return "F";
          if (!create) return "E";
          setMetadata(it);
        }
        return it[METADATA].objectID;
      };
      var getWeakData = function(it, create) {
        if (!hasOwn(it, METADATA)) {
          if (!isExtensible(it)) return true;
          if (!create) return false;
          setMetadata(it);
        }
        return it[METADATA].weakData;
      };
      var onFreeze = function(it) {
        if (FREEZING && REQUIRED && isExtensible(it) && !hasOwn(it, METADATA)) setMetadata(it);
        return it;
      };
      var enable = function() {
        meta.enable = function() {
        };
        REQUIRED = true;
        var getOwnPropertyNames = getOwnPropertyNamesModule.f;
        var splice = uncurryThis([].splice);
        var test = {};
        test[METADATA] = 1;
        if (getOwnPropertyNames(test).length) {
          getOwnPropertyNamesModule.f = function(it) {
            var result = getOwnPropertyNames(it);
            for (var i = 0, length = result.length; i < length; i++) {
              if (result[i] === METADATA) {
                splice(result, i, 1);
                break;
              }
            }
            return result;
          };
          $({ target: "Object", stat: true, forced: true }, {
            getOwnPropertyNames: getOwnPropertyNamesExternalModule.f
          });
        }
      };
      var meta = module.exports = {
        enable,
        fastKey,
        getWeakData,
        onFreeze
      };
      hiddenKeys[METADATA] = true;
    }
  });

  // node_modules/core-js-pure/internals/collection.js
  var require_collection = __commonJS({
    "node_modules/core-js-pure/internals/collection.js"(exports, module) {
      "use strict";
      var $ = require_export();
      var globalThis2 = require_global_this();
      var InternalMetadataModule = require_internal_metadata();
      var call = require_function_call();
      var fails = require_fails();
      var createNonEnumerableProperty = require_create_non_enumerable_property();
      var iterate = require_iterate();
      var anInstance = require_an_instance();
      var isCallable = require_is_callable();
      var isObject = require_is_object();
      var isNullOrUndefined = require_is_null_or_undefined();
      var setToStringTag = require_set_to_string_tag();
      var defineProperty = require_object_define_property().f;
      var forEach = require_array_iteration().forEach;
      var DESCRIPTORS = require_descriptors();
      var InternalStateModule = require_internal_state();
      var setInternalState = InternalStateModule.set;
      var internalStateGetterFor = InternalStateModule.getterFor;
      module.exports = function(CONSTRUCTOR_NAME, wrapper, common) {
        var IS_MAP = CONSTRUCTOR_NAME.indexOf("Map") !== -1;
        var IS_WEAK = CONSTRUCTOR_NAME.indexOf("Weak") !== -1;
        var ADDER = IS_MAP ? "set" : "add";
        var NativeConstructor = globalThis2[CONSTRUCTOR_NAME];
        var NativePrototype = NativeConstructor && NativeConstructor.prototype;
        var exported = {};
        var Constructor;
        if (!DESCRIPTORS || !isCallable(NativeConstructor) || !(IS_WEAK || NativePrototype.forEach && !fails(function() {
          new NativeConstructor().entries().next();
        }))) {
          Constructor = common.getConstructor(wrapper, CONSTRUCTOR_NAME, IS_MAP, ADDER);
          InternalMetadataModule.enable();
        } else {
          Constructor = wrapper(function(target, iterable) {
            setInternalState(anInstance(target, Prototype), {
              type: CONSTRUCTOR_NAME,
              collection: new NativeConstructor()
            });
            if (!isNullOrUndefined(iterable)) iterate(iterable, target[ADDER], { that: target, AS_ENTRIES: IS_MAP });
          });
          var Prototype = Constructor.prototype;
          var getInternalState = internalStateGetterFor(CONSTRUCTOR_NAME);
          forEach(["add", "clear", "delete", "forEach", "get", "has", "set", "keys", "values", "entries"], function(KEY) {
            var IS_ADDER = KEY === "add" || KEY === "set";
            if (KEY in NativePrototype && !(IS_WEAK && KEY === "clear")) {
              createNonEnumerableProperty(Prototype, KEY, function(a, b) {
                var that = this;
                var collection = getInternalState(that).collection;
                if (!IS_ADDER && IS_WEAK && !isObject(a)) return KEY === "get" ? void 0 : false;
                var result = collection[KEY](KEY === "forEach" ? function(value, key) {
                  call(a, b, value, key, that);
                } : a === 0 ? 0 : a, b);
                return IS_ADDER ? that : result;
              });
            }
          });
          IS_WEAK || defineProperty(Prototype, "size", {
            configurable: true,
            get: function() {
              return getInternalState(this).collection.size;
            }
          });
        }
        setToStringTag(Constructor, CONSTRUCTOR_NAME, false, true);
        exported[CONSTRUCTOR_NAME] = Constructor;
        $({ global: true, forced: true }, exported);
        if (!IS_WEAK) common.setStrong(Constructor, CONSTRUCTOR_NAME, IS_MAP);
        return Constructor;
      };
    }
  });

  // node_modules/core-js-pure/internals/define-built-ins.js
  var require_define_built_ins = __commonJS({
    "node_modules/core-js-pure/internals/define-built-ins.js"(exports, module) {
      "use strict";
      var defineBuiltIn = require_define_built_in();
      module.exports = function(target, src, options) {
        for (var key in src) {
          if (options && options.unsafe && target[key]) target[key] = src[key];
          else defineBuiltIn(target, key, src[key], options);
        }
        return target;
      };
    }
  });

  // node_modules/core-js-pure/internals/collection-strong.js
  var require_collection_strong = __commonJS({
    "node_modules/core-js-pure/internals/collection-strong.js"(exports, module) {
      "use strict";
      var create = require_object_create();
      var defineBuiltInAccessor = require_define_built_in_accessor();
      var defineBuiltIns = require_define_built_ins();
      var bind = require_function_bind_context();
      var anInstance = require_an_instance();
      var isNullOrUndefined = require_is_null_or_undefined();
      var iterate = require_iterate();
      var defineIterator = require_iterator_define();
      var createIterResultObject = require_create_iter_result_object();
      var setSpecies = require_set_species();
      var DESCRIPTORS = require_descriptors();
      var fastKey = require_internal_metadata().fastKey;
      var InternalStateModule = require_internal_state();
      var setInternalState = InternalStateModule.set;
      var internalStateGetterFor = InternalStateModule.getterFor;
      module.exports = {
        getConstructor: function(wrapper, CONSTRUCTOR_NAME, IS_MAP, ADDER) {
          var Constructor = wrapper(function(that, iterable) {
            anInstance(that, Prototype);
            setInternalState(that, {
              type: CONSTRUCTOR_NAME,
              index: create(null),
              first: null,
              last: null,
              size: 0
            });
            if (!DESCRIPTORS) that.size = 0;
            if (!isNullOrUndefined(iterable)) iterate(iterable, that[ADDER], { that, AS_ENTRIES: IS_MAP });
          });
          var Prototype = Constructor.prototype;
          var getInternalState = internalStateGetterFor(CONSTRUCTOR_NAME);
          var define = function(that, key, value) {
            var state = getInternalState(that);
            var entry = getEntry(that, key);
            var previous, index;
            if (entry) {
              entry.value = value;
            } else {
              state.last = entry = {
                index: index = fastKey(key, true),
                key,
                value,
                previous: previous = state.last,
                next: null,
                removed: false
              };
              if (!state.first) state.first = entry;
              if (previous) previous.next = entry;
              if (DESCRIPTORS) state.size++;
              else that.size++;
              if (index !== "F") state.index[index] = entry;
            }
            return that;
          };
          var getEntry = function(that, key) {
            var state = getInternalState(that);
            var index = fastKey(key);
            var entry;
            if (index !== "F") return state.index[index];
            for (entry = state.first; entry; entry = entry.next) {
              if (entry.key === key) return entry;
            }
          };
          defineBuiltIns(Prototype, {
            // `{ Map, Set }.prototype.clear()` methods
            // https://tc39.es/ecma262/#sec-map.prototype.clear
            // https://tc39.es/ecma262/#sec-set.prototype.clear
            clear: function clear() {
              var that = this;
              var state = getInternalState(that);
              var entry = state.first;
              while (entry) {
                entry.removed = true;
                if (entry.previous) entry.previous = entry.previous.next = null;
                entry = entry.next;
              }
              state.first = state.last = null;
              state.index = create(null);
              if (DESCRIPTORS) state.size = 0;
              else that.size = 0;
            },
            // `{ Map, Set }.prototype.delete(key)` methods
            // https://tc39.es/ecma262/#sec-map.prototype.delete
            // https://tc39.es/ecma262/#sec-set.prototype.delete
            "delete": function(key) {
              var that = this;
              var state = getInternalState(that);
              var entry = getEntry(that, key);
              if (entry) {
                var next = entry.next;
                var prev = entry.previous;
                delete state.index[entry.index];
                entry.removed = true;
                if (prev) prev.next = next;
                if (next) next.previous = prev;
                if (state.first === entry) state.first = next;
                if (state.last === entry) state.last = prev;
                if (DESCRIPTORS) state.size--;
                else that.size--;
              }
              return !!entry;
            },
            // `{ Map, Set }.prototype.forEach(callbackfn, thisArg = undefined)` methods
            // https://tc39.es/ecma262/#sec-map.prototype.foreach
            // https://tc39.es/ecma262/#sec-set.prototype.foreach
            forEach: function forEach(callbackfn) {
              var state = getInternalState(this);
              var boundFunction = bind(callbackfn, arguments.length > 1 ? arguments[1] : void 0);
              var entry;
              while (entry = entry ? entry.next : state.first) {
                boundFunction(entry.value, entry.key, this);
                while (entry && entry.removed) entry = entry.previous;
              }
            },
            // `{ Map, Set}.prototype.has(key)` methods
            // https://tc39.es/ecma262/#sec-map.prototype.has
            // https://tc39.es/ecma262/#sec-set.prototype.has
            has: function has(key) {
              return !!getEntry(this, key);
            }
          });
          defineBuiltIns(Prototype, IS_MAP ? {
            // `Map.prototype.get(key)` method
            // https://tc39.es/ecma262/#sec-map.prototype.get
            get: function get(key) {
              var entry = getEntry(this, key);
              return entry && entry.value;
            },
            // `Map.prototype.set(key, value)` method
            // https://tc39.es/ecma262/#sec-map.prototype.set
            set: function set(key, value) {
              return define(this, key === 0 ? 0 : key, value);
            }
          } : {
            // `Set.prototype.add(value)` method
            // https://tc39.es/ecma262/#sec-set.prototype.add
            add: function add(value) {
              return define(this, value = value === 0 ? 0 : value, value);
            }
          });
          if (DESCRIPTORS) defineBuiltInAccessor(Prototype, "size", {
            configurable: true,
            get: function() {
              return getInternalState(this).size;
            }
          });
          return Constructor;
        },
        setStrong: function(Constructor, CONSTRUCTOR_NAME, IS_MAP) {
          var ITERATOR_NAME = CONSTRUCTOR_NAME + " Iterator";
          var getInternalCollectionState = internalStateGetterFor(CONSTRUCTOR_NAME);
          var getInternalIteratorState = internalStateGetterFor(ITERATOR_NAME);
          defineIterator(Constructor, CONSTRUCTOR_NAME, function(iterated, kind) {
            setInternalState(this, {
              type: ITERATOR_NAME,
              target: iterated,
              state: getInternalCollectionState(iterated),
              kind,
              last: null
            });
          }, function() {
            var state = getInternalIteratorState(this);
            var kind = state.kind;
            var entry = state.last;
            while (entry && entry.removed) entry = entry.previous;
            if (!state.target || !(state.last = entry = entry ? entry.next : state.state.first)) {
              state.target = null;
              return createIterResultObject(void 0, true);
            }
            if (kind === "keys") return createIterResultObject(entry.key, false);
            if (kind === "values") return createIterResultObject(entry.value, false);
            return createIterResultObject([entry.key, entry.value], false);
          }, IS_MAP ? "entries" : "values", !IS_MAP, true);
          setSpecies(CONSTRUCTOR_NAME);
        }
      };
    }
  });

  // node_modules/core-js-pure/modules/es.map.constructor.js
  var require_es_map_constructor = __commonJS({
    "node_modules/core-js-pure/modules/es.map.constructor.js"() {
      "use strict";
      var collection = require_collection();
      var collectionStrong = require_collection_strong();
      collection("Map", function(init) {
        return function Map() {
          return init(this, arguments.length ? arguments[0] : void 0);
        };
      }, collectionStrong);
    }
  });

  // node_modules/core-js-pure/modules/es.map.js
  var require_es_map = __commonJS({
    "node_modules/core-js-pure/modules/es.map.js"() {
      "use strict";
      require_es_map_constructor();
    }
  });

  // node_modules/core-js-pure/internals/caller.js
  var require_caller = __commonJS({
    "node_modules/core-js-pure/internals/caller.js"(exports, module) {
      "use strict";
      module.exports = function(methodName, numArgs) {
        return numArgs === 1 ? function(object, arg) {
          return object[methodName](arg);
        } : function(object, arg1, arg2) {
          return object[methodName](arg1, arg2);
        };
      };
    }
  });

  // node_modules/core-js-pure/internals/map-helpers.js
  var require_map_helpers = __commonJS({
    "node_modules/core-js-pure/internals/map-helpers.js"(exports, module) {
      "use strict";
      var getBuiltIn = require_get_built_in();
      var caller = require_caller();
      var Map = getBuiltIn("Map");
      module.exports = {
        Map,
        set: caller("set", 2),
        get: caller("get", 1),
        has: caller("has", 1),
        remove: caller("delete", 1),
        proto: Map.prototype
      };
    }
  });

  // node_modules/core-js-pure/modules/es.map.group-by.js
  var require_es_map_group_by = __commonJS({
    "node_modules/core-js-pure/modules/es.map.group-by.js"() {
      "use strict";
      var $ = require_export();
      var uncurryThis = require_function_uncurry_this();
      var aCallable = require_a_callable();
      var requireObjectCoercible = require_require_object_coercible();
      var iterate = require_iterate();
      var MapHelpers = require_map_helpers();
      var IS_PURE = require_is_pure();
      var fails = require_fails();
      var Map = MapHelpers.Map;
      var has = MapHelpers.has;
      var get = MapHelpers.get;
      var set = MapHelpers.set;
      var push = uncurryThis([].push);
      var DOES_NOT_WORK_WITH_PRIMITIVES = IS_PURE || fails(function() {
        return Map.groupBy("ab", function(it) {
          return it;
        }).get("a").length !== 1;
      });
      $({ target: "Map", stat: true, forced: IS_PURE || DOES_NOT_WORK_WITH_PRIMITIVES }, {
        groupBy: function groupBy(items, callbackfn) {
          requireObjectCoercible(items);
          aCallable(callbackfn);
          var map = new Map();
          var k = 0;
          iterate(items, function(value) {
            var key = callbackfn(value, k++);
            if (!has(map, key)) set(map, key, [value]);
            else push(get(map, key), value);
          });
          return map;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.map.get-or-insert.js
  var require_es_map_get_or_insert = __commonJS({
    "node_modules/core-js-pure/modules/es.map.get-or-insert.js"() {
      "use strict";
      var $ = require_export();
      var MapHelpers = require_map_helpers();
      var IS_PURE = require_is_pure();
      var get = MapHelpers.get;
      var has = MapHelpers.has;
      var set = MapHelpers.set;
      $({ target: "Map", proto: true, real: true, forced: IS_PURE }, {
        getOrInsert: function getOrInsert(key, value) {
          if (has(this, key)) return get(this, key);
          set(this, key, value);
          return value;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/es.map.get-or-insert-computed.js
  var require_es_map_get_or_insert_computed = __commonJS({
    "node_modules/core-js-pure/modules/es.map.get-or-insert-computed.js"() {
      "use strict";
      var $ = require_export();
      var aCallable = require_a_callable();
      var MapHelpers = require_map_helpers();
      var IS_PURE = require_is_pure();
      var get = MapHelpers.get;
      var has = MapHelpers.has;
      var set = MapHelpers.set;
      $({ target: "Map", proto: true, real: true, forced: IS_PURE }, {
        getOrInsertComputed: function getOrInsertComputed(key, callbackfn) {
          var hasKey = has(this, key);
          aCallable(callbackfn);
          if (hasKey) return get(this, key);
          if (key === 0 && 1 / key === -Infinity) key = 0;
          var value = callbackfn(key);
          set(this, key, value);
          return value;
        }
      });
    }
  });

  // node_modules/core-js-pure/es/map/index.js
  var require_map8 = __commonJS({
    "node_modules/core-js-pure/es/map/index.js"(exports, module) {
      "use strict";
      require_es_array_iterator();
      require_es_map();
      require_es_map_group_by();
      require_es_map_get_or_insert();
      require_es_map_get_or_insert_computed();
      require_es_object_to_string();
      require_es_string_iterator();
      var path = require_path();
      module.exports = path.Map;
    }
  });

  // node_modules/core-js-pure/stable/map/index.js
  var require_map9 = __commonJS({
    "node_modules/core-js-pure/stable/map/index.js"(exports, module) {
      "use strict";
      var parent = require_map8();
      require_web_dom_collections_iterator();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.get-or-insert.js
  var require_esnext_map_get_or_insert = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.get-or-insert.js"() {
      "use strict";
      require_es_map_get_or_insert();
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.get-or-insert-computed.js
  var require_esnext_map_get_or_insert_computed = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.get-or-insert-computed.js"() {
      "use strict";
      require_es_map_get_or_insert_computed();
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.group-by.js
  var require_esnext_map_group_by = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.group-by.js"() {
      "use strict";
      require_es_map_group_by();
    }
  });

  // node_modules/core-js-pure/actual/map/index.js
  var require_map10 = __commonJS({
    "node_modules/core-js-pure/actual/map/index.js"(exports, module) {
      "use strict";
      var parent = require_map9();
      require_esnext_map_get_or_insert();
      require_esnext_map_get_or_insert_computed();
      require_esnext_map_group_by();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/internals/collection-from.js
  var require_collection_from = __commonJS({
    "node_modules/core-js-pure/internals/collection-from.js"(exports, module) {
      "use strict";
      var bind = require_function_bind_context();
      var anObject = require_an_object();
      var toObject = require_to_object();
      var iterate = require_iterate();
      module.exports = function(C, adder, ENTRY) {
        return function from(source) {
          var O = toObject(source);
          var length = arguments.length;
          var mapFn = length > 1 ? arguments[1] : void 0;
          var mapping = mapFn !== void 0;
          var boundFunction = mapping ? bind(mapFn, length > 2 ? arguments[2] : void 0) : void 0;
          var result = new C();
          var n = 0;
          iterate(O, function(nextItem) {
            var entry = mapping ? boundFunction(nextItem, n++) : nextItem;
            if (ENTRY) adder(result, anObject(entry)[0], entry[1]);
            else adder(result, entry);
          });
          return result;
        };
      };
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.from.js
  var require_esnext_map_from = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.from.js"() {
      "use strict";
      var $ = require_export();
      var MapHelpers = require_map_helpers();
      var createCollectionFrom = require_collection_from();
      $({ target: "Map", stat: true, forced: true }, {
        from: createCollectionFrom(MapHelpers.Map, MapHelpers.set, true)
      });
    }
  });

  // node_modules/core-js-pure/internals/collection-of.js
  var require_collection_of = __commonJS({
    "node_modules/core-js-pure/internals/collection-of.js"(exports, module) {
      "use strict";
      var anObject = require_an_object();
      module.exports = function(C, adder, ENTRY) {
        return function of() {
          var result = new C();
          var length = arguments.length;
          for (var index = 0; index < length; index++) {
            var entry = arguments[index];
            if (ENTRY) adder(result, anObject(entry)[0], entry[1]);
            else adder(result, entry);
          }
          return result;
        };
      };
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.of.js
  var require_esnext_map_of = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.of.js"() {
      "use strict";
      var $ = require_export();
      var MapHelpers = require_map_helpers();
      var createCollectionOf = require_collection_of();
      $({ target: "Map", stat: true, forced: true }, {
        of: createCollectionOf(MapHelpers.Map, MapHelpers.set, true)
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.key-by.js
  var require_esnext_map_key_by = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.key-by.js"() {
      "use strict";
      var $ = require_export();
      var call = require_function_call();
      var iterate = require_iterate();
      var isCallable = require_is_callable();
      var aCallable = require_a_callable();
      var Map = require_map_helpers().Map;
      $({ target: "Map", stat: true, forced: true }, {
        keyBy: function keyBy(iterable, keyDerivative) {
          var C = isCallable(this) ? this : Map;
          var newMap = new C();
          aCallable(keyDerivative);
          var setter = aCallable(newMap.set);
          iterate(iterable, function(element) {
            call(setter, newMap, keyDerivative(element), element);
          });
          return newMap;
        }
      });
    }
  });

  // node_modules/core-js-pure/internals/a-map.js
  var require_a_map = __commonJS({
    "node_modules/core-js-pure/internals/a-map.js"(exports, module) {
      "use strict";
      var tryToString = require_try_to_string();
      var $TypeError = TypeError;
      module.exports = function(it) {
        if (typeof it == "object" && "size" in it && "has" in it && "get" in it && "set" in it && "delete" in it && "entries" in it) return it;
        throw new $TypeError(tryToString(it) + " is not a map");
      };
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.delete-all.js
  var require_esnext_map_delete_all = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.delete-all.js"() {
      "use strict";
      var $ = require_export();
      var aMap = require_a_map();
      var remove = require_map_helpers().remove;
      $({ target: "Map", proto: true, real: true, forced: true }, {
        deleteAll: function deleteAll() {
          var collection = aMap(this);
          var allDeleted = true;
          var wasDeleted;
          for (var k = 0, len = arguments.length; k < len; k++) {
            wasDeleted = remove(collection, arguments[k]);
            allDeleted = allDeleted && wasDeleted;
          }
          return !!allDeleted;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.emplace.js
  var require_esnext_map_emplace = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.emplace.js"() {
      "use strict";
      var $ = require_export();
      var aMap = require_a_map();
      var MapHelpers = require_map_helpers();
      var get = MapHelpers.get;
      var has = MapHelpers.has;
      var set = MapHelpers.set;
      $({ target: "Map", proto: true, real: true, forced: true }, {
        emplace: function emplace(key, handler) {
          var map = aMap(this);
          var value, inserted;
          if (has(map, key)) {
            value = get(map, key);
            if ("update" in handler) {
              value = handler.update(value, key, map);
              set(map, key, value);
            }
            return value;
          }
          inserted = handler.insert(key, map);
          set(map, key, inserted);
          return inserted;
        }
      });
    }
  });

  // node_modules/core-js-pure/internals/iterate-simple.js
  var require_iterate_simple = __commonJS({
    "node_modules/core-js-pure/internals/iterate-simple.js"(exports, module) {
      "use strict";
      var call = require_function_call();
      module.exports = function(record, fn, ITERATOR_INSTEAD_OF_RECORD) {
        var iterator = ITERATOR_INSTEAD_OF_RECORD ? record : record.iterator;
        var next = record.next;
        var step, result;
        while (!(step = call(next, iterator)).done) {
          result = fn(step.value);
          if (result !== void 0) return result;
        }
      };
    }
  });

  // node_modules/core-js-pure/internals/map-iterate.js
  var require_map_iterate = __commonJS({
    "node_modules/core-js-pure/internals/map-iterate.js"(exports, module) {
      "use strict";
      var iterateSimple = require_iterate_simple();
      module.exports = function(map, fn, interruptible) {
        return interruptible ? iterateSimple(map.entries(), function(entry) {
          return fn(entry[1], entry[0]);
        }, true) : map.forEach(fn);
      };
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.every.js
  var require_esnext_map_every = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.every.js"() {
      "use strict";
      var $ = require_export();
      var bind = require_function_bind_context();
      var aMap = require_a_map();
      var iterate = require_map_iterate();
      $({ target: "Map", proto: true, real: true, forced: true }, {
        every: function every(callbackfn) {
          var map = aMap(this);
          var boundFunction = bind(callbackfn, arguments.length > 1 ? arguments[1] : void 0);
          return iterate(map, function(value, key) {
            if (!boundFunction(value, key, map)) return false;
          }, true) !== false;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.filter.js
  var require_esnext_map_filter = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.filter.js"() {
      "use strict";
      var $ = require_export();
      var bind = require_function_bind_context();
      var aMap = require_a_map();
      var MapHelpers = require_map_helpers();
      var iterate = require_map_iterate();
      var Map = MapHelpers.Map;
      var set = MapHelpers.set;
      $({ target: "Map", proto: true, real: true, forced: true }, {
        filter: function filter(callbackfn) {
          var map = aMap(this);
          var boundFunction = bind(callbackfn, arguments.length > 1 ? arguments[1] : void 0);
          var newMap = new Map();
          iterate(map, function(value, key) {
            if (boundFunction(value, key, map)) set(newMap, key, value);
          });
          return newMap;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.find.js
  var require_esnext_map_find = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.find.js"() {
      "use strict";
      var $ = require_export();
      var bind = require_function_bind_context();
      var aMap = require_a_map();
      var iterate = require_map_iterate();
      $({ target: "Map", proto: true, real: true, forced: true }, {
        find: function find(callbackfn) {
          var map = aMap(this);
          var boundFunction = bind(callbackfn, arguments.length > 1 ? arguments[1] : void 0);
          var result = iterate(map, function(value, key) {
            if (boundFunction(value, key, map)) return { value };
          }, true);
          return result && result.value;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.find-key.js
  var require_esnext_map_find_key = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.find-key.js"() {
      "use strict";
      var $ = require_export();
      var bind = require_function_bind_context();
      var aMap = require_a_map();
      var iterate = require_map_iterate();
      $({ target: "Map", proto: true, real: true, forced: true }, {
        findKey: function findKey(callbackfn) {
          var map = aMap(this);
          var boundFunction = bind(callbackfn, arguments.length > 1 ? arguments[1] : void 0);
          var result = iterate(map, function(value, key) {
            if (boundFunction(value, key, map)) return { key };
          }, true);
          return result && result.key;
        }
      });
    }
  });

  // node_modules/core-js-pure/internals/same-value-zero.js
  var require_same_value_zero = __commonJS({
    "node_modules/core-js-pure/internals/same-value-zero.js"(exports, module) {
      "use strict";
      module.exports = function(x, y) {
        return x === y || x !== x && y !== y;
      };
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.includes.js
  var require_esnext_map_includes = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.includes.js"() {
      "use strict";
      var $ = require_export();
      var sameValueZero = require_same_value_zero();
      var aMap = require_a_map();
      var iterate = require_map_iterate();
      $({ target: "Map", proto: true, real: true, forced: true }, {
        includes: function includes(searchElement) {
          return iterate(aMap(this), function(value) {
            if (sameValueZero(value, searchElement)) return true;
          }, true) === true;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.key-of.js
  var require_esnext_map_key_of = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.key-of.js"() {
      "use strict";
      var $ = require_export();
      var aMap = require_a_map();
      var iterate = require_map_iterate();
      $({ target: "Map", proto: true, real: true, forced: true }, {
        keyOf: function keyOf(searchElement) {
          var result = iterate(aMap(this), function(value, key) {
            if (value === searchElement) return { key };
          }, true);
          return result && result.key;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.map-keys.js
  var require_esnext_map_map_keys = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.map-keys.js"() {
      "use strict";
      var $ = require_export();
      var bind = require_function_bind_context();
      var aMap = require_a_map();
      var MapHelpers = require_map_helpers();
      var iterate = require_map_iterate();
      var Map = MapHelpers.Map;
      var set = MapHelpers.set;
      $({ target: "Map", proto: true, real: true, forced: true }, {
        mapKeys: function mapKeys(callbackfn) {
          var map = aMap(this);
          var boundFunction = bind(callbackfn, arguments.length > 1 ? arguments[1] : void 0);
          var newMap = new Map();
          iterate(map, function(value, key) {
            set(newMap, boundFunction(value, key, map), value);
          });
          return newMap;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.map-values.js
  var require_esnext_map_map_values = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.map-values.js"() {
      "use strict";
      var $ = require_export();
      var bind = require_function_bind_context();
      var aMap = require_a_map();
      var MapHelpers = require_map_helpers();
      var iterate = require_map_iterate();
      var Map = MapHelpers.Map;
      var set = MapHelpers.set;
      $({ target: "Map", proto: true, real: true, forced: true }, {
        mapValues: function mapValues(callbackfn) {
          var map = aMap(this);
          var boundFunction = bind(callbackfn, arguments.length > 1 ? arguments[1] : void 0);
          var newMap = new Map();
          iterate(map, function(value, key) {
            set(newMap, key, boundFunction(value, key, map));
          });
          return newMap;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.merge.js
  var require_esnext_map_merge = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.merge.js"() {
      "use strict";
      var $ = require_export();
      var aMap = require_a_map();
      var iterate = require_iterate();
      var set = require_map_helpers().set;
      $({ target: "Map", proto: true, real: true, arity: 1, forced: true }, {
        // eslint-disable-next-line no-unused-vars -- required for `.length`
        merge: function merge(iterable) {
          var map = aMap(this);
          var argumentsLength = arguments.length;
          var i = 0;
          while (i < argumentsLength) {
            iterate(arguments[i++], function(key, value) {
              set(map, key, value);
            }, { AS_ENTRIES: true });
          }
          return map;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.reduce.js
  var require_esnext_map_reduce = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.reduce.js"() {
      "use strict";
      var $ = require_export();
      var aCallable = require_a_callable();
      var aMap = require_a_map();
      var iterate = require_map_iterate();
      var $TypeError = TypeError;
      $({ target: "Map", proto: true, real: true, forced: true }, {
        reduce: function reduce(callbackfn) {
          var map = aMap(this);
          var noInitial = arguments.length < 2;
          var accumulator = noInitial ? void 0 : arguments[1];
          aCallable(callbackfn);
          iterate(map, function(value, key) {
            if (noInitial) {
              noInitial = false;
              accumulator = value;
            } else {
              accumulator = callbackfn(accumulator, value, key, map);
            }
          });
          if (noInitial) throw new $TypeError("Reduce of empty map with no initial value");
          return accumulator;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.some.js
  var require_esnext_map_some = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.some.js"() {
      "use strict";
      var $ = require_export();
      var bind = require_function_bind_context();
      var aMap = require_a_map();
      var iterate = require_map_iterate();
      $({ target: "Map", proto: true, real: true, forced: true }, {
        some: function some(callbackfn) {
          var map = aMap(this);
          var boundFunction = bind(callbackfn, arguments.length > 1 ? arguments[1] : void 0);
          return iterate(map, function(value, key) {
            if (boundFunction(value, key, map)) return true;
          }, true) === true;
        }
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.update.js
  var require_esnext_map_update = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.update.js"() {
      "use strict";
      var $ = require_export();
      var aCallable = require_a_callable();
      var aMap = require_a_map();
      var MapHelpers = require_map_helpers();
      var $TypeError = TypeError;
      var get = MapHelpers.get;
      var has = MapHelpers.has;
      var set = MapHelpers.set;
      $({ target: "Map", proto: true, real: true, forced: true }, {
        update: function update(key, callback) {
          var map = aMap(this);
          var length = arguments.length;
          aCallable(callback);
          var isPresentInMap = has(map, key);
          if (!isPresentInMap && length < 3) {
            throw new $TypeError("Updating absent value");
          }
          var value = isPresentInMap ? get(map, key) : aCallable(length > 2 ? arguments[2] : void 0)(key, map);
          set(map, key, callback(value, key, map));
          return map;
        }
      });
    }
  });

  // node_modules/core-js-pure/internals/map-upsert.js
  var require_map_upsert = __commonJS({
    "node_modules/core-js-pure/internals/map-upsert.js"(exports, module) {
      "use strict";
      var call = require_function_call();
      var aCallable = require_a_callable();
      var isCallable = require_is_callable();
      var anObject = require_an_object();
      var $TypeError = TypeError;
      module.exports = function upsert(key, updateFn) {
        var map = anObject(this);
        var get = aCallable(map.get);
        var has = aCallable(map.has);
        var set = aCallable(map.set);
        var insertFn = arguments.length > 2 ? arguments[2] : void 0;
        var value;
        if (!isCallable(updateFn) && !isCallable(insertFn)) {
          throw new $TypeError("At least one callback required");
        }
        if (call(has, map, key)) {
          value = call(get, map, key);
          if (isCallable(updateFn)) {
            value = updateFn(value);
            call(set, map, key, value);
          }
        } else if (isCallable(insertFn)) {
          value = insertFn();
          call(set, map, key, value);
        }
        return value;
      };
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.upsert.js
  var require_esnext_map_upsert = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.upsert.js"() {
      "use strict";
      var $ = require_export();
      var upsert = require_map_upsert();
      $({ target: "Map", proto: true, real: true, forced: true }, {
        upsert
      });
    }
  });

  // node_modules/core-js-pure/modules/esnext.map.update-or-insert.js
  var require_esnext_map_update_or_insert = __commonJS({
    "node_modules/core-js-pure/modules/esnext.map.update-or-insert.js"() {
      "use strict";
      var $ = require_export();
      var upsert = require_map_upsert();
      $({ target: "Map", proto: true, real: true, name: "upsert", forced: true }, {
        updateOrInsert: upsert
      });
    }
  });

  // node_modules/core-js-pure/full/map/index.js
  var require_map11 = __commonJS({
    "node_modules/core-js-pure/full/map/index.js"(exports, module) {
      "use strict";
      var parent = require_map10();
      require_esnext_map_from();
      require_esnext_map_of();
      require_esnext_map_key_by();
      require_esnext_map_delete_all();
      require_esnext_map_emplace();
      require_esnext_map_every();
      require_esnext_map_filter();
      require_esnext_map_find();
      require_esnext_map_find_key();
      require_esnext_map_includes();
      require_esnext_map_get_or_insert();
      require_esnext_map_get_or_insert_computed();
      require_esnext_map_key_of();
      require_esnext_map_map_keys();
      require_esnext_map_map_values();
      require_esnext_map_merge();
      require_esnext_map_reduce();
      require_esnext_map_some();
      require_esnext_map_update();
      require_esnext_map_upsert();
      require_esnext_map_update_or_insert();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/map/index.js
  var require_map12 = __commonJS({
    "node_modules/core-js-pure/features/map/index.js"(exports, module) {
      "use strict";
      module.exports = require_map11();
    }
  });

  // node_modules/core-js-pure/modules/es.array.index-of.js
  var require_es_array_index_of = __commonJS({
    "node_modules/core-js-pure/modules/es.array.index-of.js"() {
      "use strict";
      var $ = require_export();
      var uncurryThis = require_function_uncurry_this_clause();
      var $indexOf = require_array_includes().indexOf;
      var arrayMethodIsStrict = require_array_method_is_strict();
      var nativeIndexOf = uncurryThis([].indexOf);
      var NEGATIVE_ZERO = !!nativeIndexOf && 1 / nativeIndexOf([1], 1, -0) < 0;
      var FORCED = NEGATIVE_ZERO || !arrayMethodIsStrict("indexOf");
      $({ target: "Array", proto: true, forced: FORCED }, {
        indexOf: function indexOf(searchElement) {
          var fromIndex = arguments.length > 1 ? arguments[1] : void 0;
          return NEGATIVE_ZERO ? nativeIndexOf(this, searchElement, fromIndex) || 0 : $indexOf(this, searchElement, fromIndex);
        }
      });
    }
  });

  // node_modules/core-js-pure/es/array/virtual/index-of.js
  var require_index_of = __commonJS({
    "node_modules/core-js-pure/es/array/virtual/index-of.js"(exports, module) {
      "use strict";
      require_es_array_index_of();
      var getBuiltInPrototypeMethod = require_get_built_in_prototype_method();
      module.exports = getBuiltInPrototypeMethod("Array", "indexOf");
    }
  });

  // node_modules/core-js-pure/es/instance/index-of.js
  var require_index_of2 = __commonJS({
    "node_modules/core-js-pure/es/instance/index-of.js"(exports, module) {
      "use strict";
      var isPrototypeOf = require_object_is_prototype_of();
      var method = require_index_of();
      var ArrayPrototype = Array.prototype;
      module.exports = function(it) {
        var own = it.indexOf;
        return it === ArrayPrototype || isPrototypeOf(ArrayPrototype, it) && own === ArrayPrototype.indexOf ? method : own;
      };
    }
  });

  // node_modules/core-js-pure/stable/instance/index-of.js
  var require_index_of3 = __commonJS({
    "node_modules/core-js-pure/stable/instance/index-of.js"(exports, module) {
      "use strict";
      var parent = require_index_of2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/instance/index-of.js
  var require_index_of4 = __commonJS({
    "node_modules/core-js-pure/actual/instance/index-of.js"(exports, module) {
      "use strict";
      var parent = require_index_of3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/instance/index-of.js
  var require_index_of5 = __commonJS({
    "node_modules/core-js-pure/full/instance/index-of.js"(exports, module) {
      "use strict";
      var parent = require_index_of4();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/instance/index-of.js
  var require_index_of6 = __commonJS({
    "node_modules/core-js-pure/features/instance/index-of.js"(exports, module) {
      "use strict";
      module.exports = require_index_of5();
    }
  });

  // node_modules/core-js-pure/modules/es.object.keys.js
  var require_es_object_keys = __commonJS({
    "node_modules/core-js-pure/modules/es.object.keys.js"() {
      "use strict";
      var $ = require_export();
      var toObject = require_to_object();
      var nativeKeys = require_object_keys();
      var fails = require_fails();
      var FAILS_ON_PRIMITIVES = fails(function() {
        nativeKeys(1);
      });
      $({ target: "Object", stat: true, forced: FAILS_ON_PRIMITIVES }, {
        keys: function keys(it) {
          return nativeKeys(toObject(it));
        }
      });
    }
  });

  // node_modules/core-js-pure/es/object/keys.js
  var require_keys = __commonJS({
    "node_modules/core-js-pure/es/object/keys.js"(exports, module) {
      "use strict";
      require_es_object_keys();
      var path = require_path();
      module.exports = path.Object.keys;
    }
  });

  // node_modules/core-js-pure/stable/object/keys.js
  var require_keys2 = __commonJS({
    "node_modules/core-js-pure/stable/object/keys.js"(exports, module) {
      "use strict";
      var parent = require_keys();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/actual/object/keys.js
  var require_keys3 = __commonJS({
    "node_modules/core-js-pure/actual/object/keys.js"(exports, module) {
      "use strict";
      var parent = require_keys2();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/full/object/keys.js
  var require_keys4 = __commonJS({
    "node_modules/core-js-pure/full/object/keys.js"(exports, module) {
      "use strict";
      var parent = require_keys3();
      module.exports = parent;
    }
  });

  // node_modules/core-js-pure/features/object/keys.js
  var require_keys5 = __commonJS({
    "node_modules/core-js-pure/features/object/keys.js"(exports, module) {
      "use strict";
      module.exports = require_keys4();
    }
  });

  // node_modules/@babel/runtime-corejs3/core-js/object/keys.js
  var require_keys6 = __commonJS({
    "node_modules/@babel/runtime-corejs3/core-js/object/keys.js"(exports, module) {
      module.exports = require_keys5();
    }
  });

  // entry.js
  var entry_exports = {};
  __export(entry_exports, {
    createTextQuoteSelectorMatcher: () => createTextQuoteSelectorMatcher,
    describeTextQuote: () => describeTextQuote2,
    highlightText: () => highlightText
  });

  // node_modules/@apache-annotator/dom/lib/css.js
  var import_slice = __toESM(require_slice7(), 1);
  var import_from = __toESM(require_from6(), 1);
  var import_symbol2 = __toESM(require_symbol6(), 1);
  var import_get_iterator_method = __toESM(require_get_iterator_method7(), 1);
  var import_get_iterator = __toESM(require_get_iterator7(), 1);

  // node_modules/@babel/runtime-corejs3/helpers/esm/asyncToGenerator.js
  var import_promise = __toESM(require_promise5(), 1);
  function asyncGeneratorStep(n, t, e, r, o, a, c) {
    try {
      var i = n[a](c), u = i.value;
    } catch (n2) {
      return void e(n2);
    }
    i.done ? t(u) : import_promise.default.resolve(u).then(r, o);
  }
  function _asyncToGenerator(n) {
    return function() {
      var t = this, e = arguments;
      return new import_promise.default(function(r, o) {
        var a = n.apply(t, e);
        function _next(n2) {
          asyncGeneratorStep(a, r, o, _next, _throw, "next", n2);
        }
        function _throw(n2) {
          asyncGeneratorStep(a, r, o, _next, _throw, "throw", n2);
        }
        _next(void 0);
      });
    };
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/OverloadYield.js
  function _OverloadYield(e, d) {
    this.v = e, this.k = d;
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/awaitAsyncGenerator.js
  function _awaitAsyncGenerator(e) {
    return new _OverloadYield(e, 0);
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/wrapAsyncGenerator.js
  var import_promise2 = __toESM(require_promise5(), 1);
  var import_symbol = __toESM(require_symbol5(), 1);
  var import_async_iterator = __toESM(require_async_iterator5(), 1);
  function _wrapAsyncGenerator(e) {
    return function() {
      return new AsyncGenerator(e.apply(this, arguments));
    };
  }
  function AsyncGenerator(e) {
    var t, n;
    function resume(t2, n2) {
      try {
        var r = e[t2](n2), o = r.value, u = o instanceof _OverloadYield;
        import_promise2.default.resolve(u ? o.v : o).then(function(n3) {
          if (u) {
            var i = "return" === t2 && o.k ? t2 : "next";
            if (!o.k || n3.done) return resume(i, n3);
            n3 = e[i](n3).value;
          }
          settle(!!r.done, n3);
        }, function(e2) {
          resume("throw", e2);
        });
      } catch (e2) {
        settle(2, e2);
      }
    }
    function settle(e2, r) {
      2 === e2 ? t.reject(r) : t.resolve({
        value: r,
        done: e2
      }), (t = t.next) ? resume(t.key, t.arg) : n = null;
    }
    this._invoke = function(e2, r) {
      return new import_promise2.default(function(o, u) {
        var i = {
          key: e2,
          arg: r,
          resolve: o,
          reject: u,
          next: null
        };
        n ? n = n.next = i : (t = n = i, resume(e2, r));
      });
    }, "function" != typeof e["return"] && (this["return"] = void 0);
  }
  AsyncGenerator.prototype["function" == typeof import_symbol.default && import_async_iterator.default || "@@asyncIterator"] = function() {
    return this;
  }, AsyncGenerator.prototype.next = function(e) {
    return this._invoke("next", e);
  }, AsyncGenerator.prototype["throw"] = function(e) {
    return this._invoke("throw", e);
  }, AsyncGenerator.prototype["return"] = function(e) {
    return this._invoke("return", e);
  };

  // node_modules/@apache-annotator/dom/lib/css.js
  var import_regenerator = __toESM(require_regenerator2(), 1);
  var import_optimal_select = __toESM(require_lib(), 1);

  // node_modules/@apache-annotator/dom/lib/owner-document.js
  function ownerDocument(nodeOrRange) {
    var _node$ownerDocument;
    var node = isRange(nodeOrRange) ? nodeOrRange.startContainer : nodeOrRange;
    return (_node$ownerDocument = node.ownerDocument) !== null && _node$ownerDocument !== void 0 ? _node$ownerDocument : node;
  }
  function isRange(nodeOrRange) {
    return "startContainer" in nodeOrRange;
  }

  // node_modules/@apache-annotator/dom/lib/to-range.js
  function toRange(nodeOrRange) {
    if (isRange2(nodeOrRange)) {
      return nodeOrRange;
    } else {
      var node = nodeOrRange;
      var range = ownerDocument(node).createRange();
      range.selectNodeContents(node);
      return range;
    }
  }
  function isRange2(nodeOrRange) {
    return "startContainer" in nodeOrRange;
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/arrayWithHoles.js
  var import_is_array = __toESM(require_is_array6(), 1);
  function _arrayWithHoles(r) {
    if ((0, import_is_array.default)(r)) return r;
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/iterableToArrayLimit.js
  var import_symbol3 = __toESM(require_symbol5(), 1);
  var import_get_iterator_method2 = __toESM(require_get_iterator_method6(), 1);
  var import_push = __toESM(require_push6(), 1);
  function _iterableToArrayLimit(r, l) {
    var t = null == r ? null : "undefined" != typeof import_symbol3.default && (0, import_get_iterator_method2.default)(r) || r["@@iterator"];
    if (null != t) {
      var e, n, i, u, a = [], f = true, o = false;
      try {
        if (i = (t = t.call(r)).next, 0 === l) {
          if (Object(t) !== t) return;
          f = false;
        } else for (; !(f = (e = i.call(t)).done) && ((0, import_push.default)(a).call(a, e.value), a.length !== l); f = true) ;
      } catch (r2) {
        o = true, n = r2;
      } finally {
        try {
          if (!f && null != t["return"] && (u = t["return"](), Object(u) !== u)) return;
        } finally {
          if (o) throw n;
        }
      }
      return a;
    }
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/unsupportedIterableToArray.js
  var import_slice2 = __toESM(require_slice6(), 1);
  var import_from2 = __toESM(require_from5(), 1);

  // node_modules/@babel/runtime-corejs3/helpers/esm/arrayLikeToArray.js
  function _arrayLikeToArray(r, a) {
    (null == a || a > r.length) && (a = r.length);
    for (var e = 0, n = Array(a); e < a; e++) n[e] = r[e];
    return n;
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/unsupportedIterableToArray.js
  function _unsupportedIterableToArray(r, a) {
    if (r) {
      var _context;
      if ("string" == typeof r) return _arrayLikeToArray(r, a);
      var t = (0, import_slice2.default)(_context = {}.toString.call(r)).call(_context, 8, -1);
      return "Object" === t && r.constructor && (t = r.constructor.name), "Map" === t || "Set" === t ? (0, import_from2.default)(r) : "Arguments" === t || /^(?:Ui|I)nt(?:8|16|32)(?:Clamped)?Array$/.test(t) ? _arrayLikeToArray(r, a) : void 0;
    }
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/nonIterableRest.js
  function _nonIterableRest() {
    throw new TypeError("Invalid attempt to destructure non-iterable instance.\nIn order to be iterable, non-array objects must have a [Symbol.iterator]() method.");
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/slicedToArray.js
  function _slicedToArray(r, e) {
    return _arrayWithHoles(r) || _iterableToArrayLimit(r, e) || _unsupportedIterableToArray(r, e) || _nonIterableRest();
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/asyncIterator.js
  var import_symbol4 = __toESM(require_symbol5(), 1);
  var import_async_iterator2 = __toESM(require_async_iterator5(), 1);
  var import_iterator = __toESM(require_iterator5(), 1);
  var import_promise3 = __toESM(require_promise5(), 1);
  function _asyncIterator(r) {
    var n, t, o, e = 2;
    for ("undefined" != typeof import_symbol4.default && (t = import_async_iterator2.default, o = import_iterator.default); e--; ) {
      if (t && null != (n = r[t])) return n.call(r);
      if (o && null != (n = r[o])) return new AsyncFromSyncIterator(n.call(r));
      t = "@@asyncIterator", o = "@@iterator";
    }
    throw new TypeError("Object is not async iterable");
  }
  function AsyncFromSyncIterator(r) {
    function AsyncFromSyncIteratorContinuation(r2) {
      if (Object(r2) !== r2) return import_promise3.default.reject(new TypeError(r2 + " is not an object."));
      var n = r2.done;
      return import_promise3.default.resolve(r2.value).then(function(r3) {
        return {
          value: r3,
          done: n
        };
      });
    }
    return AsyncFromSyncIterator = function AsyncFromSyncIterator2(r2) {
      this.s = r2, this.n = r2.next;
    }, AsyncFromSyncIterator.prototype = {
      s: null,
      n: null,
      next: function next() {
        return AsyncFromSyncIteratorContinuation(this.n.apply(this.s, arguments));
      },
      "return": function _return(r2) {
        var n = this.s["return"];
        return void 0 === n ? import_promise3.default.resolve({
          value: r2,
          done: true
        }) : AsyncFromSyncIteratorContinuation(n.apply(this.s, arguments));
      },
      "throw": function _throw(r2) {
        var n = this.s["return"];
        return void 0 === n ? import_promise3.default.reject(r2) : AsyncFromSyncIteratorContinuation(n.apply(this.s, arguments));
      }
    }, new AsyncFromSyncIterator(r);
  }

  // node_modules/@apache-annotator/dom/lib/range/match.js
  var import_regenerator3 = __toESM(require_regenerator2(), 1);

  // node_modules/@apache-annotator/dom/lib/range/cartesian.js
  var import_regenerator2 = __toESM(require_regenerator2(), 1);
  var import_map = __toESM(require_map7(), 1);
  var import_promise4 = __toESM(require_promise6(), 1);
  var import_reduce = __toESM(require_reduce7(), 1);
  var import_flat_map = __toESM(require_flat_map7(), 1);
  var import_concat = __toESM(require_concat7(), 1);

  // node_modules/@apache-annotator/dom/lib/text-quote/describe.js
  var import_regenerator9 = __toESM(require_regenerator2(), 1);

  // node_modules/@apache-annotator/selector/lib/index.js
  var import_regenerator8 = __toESM(require_regenerator2(), 1);

  // node_modules/@apache-annotator/selector/lib/text/describe-text-quote.js
  var import_regenerator4 = __toESM(require_regenerator2(), 1);

  // node_modules/@apache-annotator/selector/lib/text/chunker.js
  function chunkEquals(chunk1, chunk2) {
    if (chunk1.equals) return chunk1.equals(chunk2);
    if (chunk2.equals) return chunk2.equals(chunk1);
    return chunk1 === chunk2;
  }
  function chunkRangeEquals(range1, range2) {
    return chunkEquals(range1.startChunk, range2.startChunk) && chunkEquals(range1.endChunk, range2.endChunk) && range1.startIndex === range2.startIndex && range1.endIndex === range2.endIndex;
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/classCallCheck.js
  function _classCallCheck(a, n) {
    if (!(a instanceof n)) throw new TypeError("Cannot call a class as a function");
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/createClass.js
  var import_define_property = __toESM(require_define_property5(), 1);

  // node_modules/@babel/runtime-corejs3/helpers/esm/typeof.js
  var import_symbol5 = __toESM(require_symbol5(), 1);
  var import_iterator2 = __toESM(require_iterator5(), 1);
  function _typeof(o) {
    "@babel/helpers - typeof";
    return _typeof = "function" == typeof import_symbol5.default && "symbol" == typeof import_iterator2.default ? function(o2) {
      return typeof o2;
    } : function(o2) {
      return o2 && "function" == typeof import_symbol5.default && o2.constructor === import_symbol5.default && o2 !== import_symbol5.default.prototype ? "symbol" : typeof o2;
    }, _typeof(o);
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/toPrimitive.js
  var import_to_primitive = __toESM(require_to_primitive6(), 1);
  function toPrimitive(t, r) {
    if ("object" != _typeof(t) || !t) return t;
    var e = t[import_to_primitive.default];
    if (void 0 !== e) {
      var i = e.call(t, r || "default");
      if ("object" != _typeof(i)) return i;
      throw new TypeError("@@toPrimitive must return a primitive value.");
    }
    return ("string" === r ? String : Number)(t);
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/toPropertyKey.js
  function toPropertyKey(t) {
    var i = toPrimitive(t, "string");
    return "symbol" == _typeof(i) ? i : i + "";
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/createClass.js
  function _defineProperties(e, r) {
    for (var t = 0; t < r.length; t++) {
      var o = r[t];
      o.enumerable = o.enumerable || false, o.configurable = true, "value" in o && (o.writable = true), (0, import_define_property.default)(e, toPropertyKey(o.key), o);
    }
  }
  function _createClass(e, r, t) {
    return r && _defineProperties(e.prototype, r), t && _defineProperties(e, t), (0, import_define_property.default)(e, "prototype", {
      writable: false
    }), e;
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/defineProperty.js
  var import_define_property2 = __toESM(require_define_property5(), 1);
  function _defineProperty(e, r, t) {
    return (r = toPropertyKey(r)) in e ? (0, import_define_property2.default)(e, r, {
      value: t,
      enumerable: true,
      configurable: true,
      writable: true
    }) : e[r] = t, e;
  }

  // node_modules/@apache-annotator/selector/lib/text/seeker.js
  var import_slice3 = __toESM(require_slice7(), 1);
  var E_END = "Iterator exhausted before seek ended.";
  var TextSeeker = /* @__PURE__ */ (function() {
    function TextSeeker2(chunker) {
      _classCallCheck(this, TextSeeker2);
      this.chunker = chunker;
      _defineProperty(this, "currentChunkPosition", 0);
      _defineProperty(this, "offsetInChunk", 0);
      this.seekTo(0);
    }
    _createClass(TextSeeker2, [{
      key: "currentChunk",
      get: (
        // The chunk containing our current text position.
        function get() {
          return this.chunker.currentChunk;
        }
      )
      // The index of the first character of the current chunk inside the text.
    }, {
      key: "position",
      get: (
        // The current text position (measured in code units)
        function get() {
          return this.currentChunkPosition + this.offsetInChunk;
        }
      )
    }, {
      key: "read",
      value: function read(length) {
        var roundUp = arguments.length > 1 && arguments[1] !== void 0 ? arguments[1] : false;
        var lessIsFine = arguments.length > 2 && arguments[2] !== void 0 ? arguments[2] : false;
        return this._readOrSeekTo(true, this.position + length, roundUp, lessIsFine);
      }
    }, {
      key: "readTo",
      value: function readTo(target) {
        var roundUp = arguments.length > 1 && arguments[1] !== void 0 ? arguments[1] : false;
        return this._readOrSeekTo(true, target, roundUp);
      }
    }, {
      key: "seekBy",
      value: function seekBy(length) {
        this.seekTo(this.position + length);
      }
    }, {
      key: "seekTo",
      value: function seekTo(target) {
        this._readOrSeekTo(false, target);
      }
    }, {
      key: "seekToChunk",
      value: function seekToChunk(target) {
        var offset = arguments.length > 1 && arguments[1] !== void 0 ? arguments[1] : 0;
        this._readOrSeekToChunk(false, target, offset);
      }
    }, {
      key: "readToChunk",
      value: function readToChunk(target) {
        var offset = arguments.length > 1 && arguments[1] !== void 0 ? arguments[1] : 0;
        return this._readOrSeekToChunk(true, target, offset);
      }
    }, {
      key: "_readOrSeekToChunk",
      value: function _readOrSeekToChunk(read, target) {
        var offset = arguments.length > 2 && arguments[2] !== void 0 ? arguments[2] : 0;
        var oldPosition = this.position;
        var result = "";
        if (!this.chunker.precedesCurrentChunk(target)) {
          while (!chunkEquals(this.currentChunk, target)) {
            var _this$_readToNextChun = this._readToNextChunk(), _this$_readToNextChun2 = _slicedToArray(_this$_readToNextChun, 2), data = _this$_readToNextChun2[0], nextChunk = _this$_readToNextChun2[1];
            if (read) result += data;
            if (nextChunk === null) throw new RangeError(E_END);
          }
        } else {
          while (!chunkEquals(this.currentChunk, target)) {
            var _this$_readToPrevious = this._readToPreviousChunk(), _this$_readToPrevious2 = _slicedToArray(_this$_readToPrevious, 2), _data = _this$_readToPrevious2[0], previousChunk = _this$_readToPrevious2[1];
            if (read) result = _data + result;
            if (previousChunk === null) throw new RangeError(E_END);
          }
        }
        var targetPosition = this.currentChunkPosition + offset;
        if (!read) {
          this.seekTo(targetPosition);
        } else {
          if (targetPosition >= this.position) {
            result += this.readTo(targetPosition);
          } else if (targetPosition >= oldPosition) {
            this.seekTo(targetPosition);
            result = (0, import_slice3.default)(result).call(result, 0, targetPosition - oldPosition);
          } else {
            this.seekTo(oldPosition);
            result = this.readTo(targetPosition);
          }
          return result;
        }
      }
    }, {
      key: "_readOrSeekTo",
      value: function _readOrSeekTo(read, target) {
        var roundUp = arguments.length > 2 && arguments[2] !== void 0 ? arguments[2] : false;
        var lessIsFine = arguments.length > 3 && arguments[3] !== void 0 ? arguments[3] : false;
        var result = "";
        if (this.position <= target) {
          while (true) {
            var endOfChunk = this.currentChunkPosition + this.currentChunk.data.length;
            if (endOfChunk <= target) {
              var _this$_readToNextChun3 = this._readToNextChunk(), _this$_readToNextChun4 = _slicedToArray(_this$_readToNextChun3, 2), data = _this$_readToNextChun4[0], nextChunk = _this$_readToNextChun4[1];
              if (read) result += data;
              if (nextChunk === null) {
                if (this.position === target || lessIsFine) break;
                else throw new RangeError(E_END);
              }
            } else {
              var newOffset = roundUp ? this.currentChunk.data.length : target - this.currentChunkPosition;
              if (read) result += this.currentChunk.data.substring(this.offsetInChunk, newOffset);
              this.offsetInChunk = newOffset;
              if (roundUp) this.seekBy(0);
              break;
            }
          }
        } else {
          while (this.position > target) {
            if (this.currentChunkPosition <= target) {
              var _newOffset = roundUp ? 0 : target - this.currentChunkPosition;
              if (read) result = this.currentChunk.data.substring(_newOffset, this.offsetInChunk) + result;
              this.offsetInChunk = _newOffset;
              break;
            } else {
              var _this$_readToPrevious3 = this._readToPreviousChunk(), _this$_readToPrevious4 = _slicedToArray(_this$_readToPrevious3, 2), _data2 = _this$_readToPrevious4[0], previousChunk = _this$_readToPrevious4[1];
              if (read) result = _data2 + result;
              if (previousChunk === null) {
                if (lessIsFine) break;
                else throw new RangeError(E_END);
              }
            }
          }
        }
        if (read) return result;
      }
      // Read to the start of the next chunk, if any; otherwise to the end of the current chunk.
    }, {
      key: "_readToNextChunk",
      value: function _readToNextChunk() {
        var data = this.currentChunk.data.substring(this.offsetInChunk);
        var chunkLength = this.currentChunk.data.length;
        var nextChunk = this.chunker.nextChunk();
        if (nextChunk !== null) {
          this.currentChunkPosition += chunkLength;
          this.offsetInChunk = 0;
        } else {
          this.offsetInChunk = chunkLength;
        }
        return [data, nextChunk];
      }
      // Read backwards to the end of the previous chunk, if any; otherwise to the start of the current chunk.
    }, {
      key: "_readToPreviousChunk",
      value: function _readToPreviousChunk() {
        var data = this.currentChunk.data.substring(0, this.offsetInChunk);
        var previousChunk = this.chunker.previousChunk();
        if (previousChunk !== null) {
          this.currentChunkPosition -= this.currentChunk.data.length;
          this.offsetInChunk = this.currentChunk.data.length;
        } else {
          this.offsetInChunk = 0;
        }
        return [data, previousChunk];
      }
    }]);
    return TextSeeker2;
  })();

  // node_modules/@apache-annotator/selector/lib/text/describe-text-quote.js
  function describeTextQuote(_x, _x2) {
    return _describeTextQuote.apply(this, arguments);
  }
  function _describeTextQuote() {
    _describeTextQuote = _asyncToGenerator(/* @__PURE__ */ import_regenerator4.default.mark(function _callee(target, scope) {
      var options, _options$minimalConte, minimalContext, _options$minimumQuote, minimumQuoteLength, _options$maxWordLengt, maxWordLength, seekerAtTarget, seekerAtUnintendedMatch, exact, prefix, suffix, currentQuoteLength, length, _length, _length2, tentativeSelector, matches, nextMatch, unintendedMatch, extraPrefix, extraSuffix, _args = arguments;
      return import_regenerator4.default.wrap(function _callee$(_context) {
        while (1) {
          switch (_context.prev = _context.next) {
            case 0:
              options = _args.length > 2 && _args[2] !== void 0 ? _args[2] : {};
              _options$minimalConte = options.minimalContext, minimalContext = _options$minimalConte === void 0 ? false : _options$minimalConte, _options$minimumQuote = options.minimumQuoteLength, minimumQuoteLength = _options$minimumQuote === void 0 ? 0 : _options$minimumQuote, _options$maxWordLengt = options.maxWordLength, maxWordLength = _options$maxWordLengt === void 0 ? 50 : _options$maxWordLengt;
              seekerAtTarget = new TextSeeker(scope());
              seekerAtUnintendedMatch = new TextSeeker(scope());
              seekerAtTarget.seekToChunk(target.startChunk, target.startIndex);
              exact = seekerAtTarget.readToChunk(target.endChunk, target.endIndex);
              prefix = "";
              suffix = "";
              currentQuoteLength = function currentQuoteLength2() {
                return prefix.length + exact.length + suffix.length;
              };
              if (currentQuoteLength() < minimumQuoteLength) {
                seekerAtTarget.seekToChunk(target.startChunk, target.startIndex - prefix.length);
                length = Math.floor((minimumQuoteLength - currentQuoteLength()) / 2);
                prefix = seekerAtTarget.read(-length, false, true) + prefix;
                if (currentQuoteLength() < minimumQuoteLength) {
                  seekerAtTarget.seekToChunk(target.endChunk, target.endIndex + suffix.length);
                  _length = minimumQuoteLength - currentQuoteLength();
                  suffix = suffix + seekerAtTarget.read(_length, false, true);
                  if (currentQuoteLength() < minimumQuoteLength) {
                    seekerAtTarget.seekToChunk(target.startChunk, target.startIndex - prefix.length);
                    _length2 = minimumQuoteLength - currentQuoteLength();
                    prefix = seekerAtTarget.read(-_length2, false, true) + prefix;
                  }
                }
              }
              if (!minimalContext) {
                seekerAtTarget.seekToChunk(target.startChunk, target.startIndex - prefix.length);
                prefix = readUntilWhitespace(seekerAtTarget, maxWordLength, true) + prefix;
                seekerAtTarget.seekToChunk(target.endChunk, target.endIndex + suffix.length);
                suffix = suffix + readUntilWhitespace(seekerAtTarget, maxWordLength, false);
              }
            // Search for matches of the quote using the current prefix and suffix. At
            // each unintended match we encounter, we extend the prefix or suffix to
            // ensure it will no longer match.
            case 11:
              if (false) {
                _context.next = 48;
                break;
              }
              tentativeSelector = {
                type: "TextQuoteSelector",
                exact,
                prefix,
                suffix
              };
              matches = textQuoteSelectorMatcher(tentativeSelector)(scope());
              _context.next = 16;
              return matches.next();
            case 16:
              nextMatch = _context.sent;
              if (!(!nextMatch.done && chunkRangeEquals(nextMatch.value, target))) {
                _context.next = 21;
                break;
              }
              _context.next = 20;
              return matches.next();
            case 20:
              nextMatch = _context.sent;
            case 21:
              if (!nextMatch.done) {
                _context.next = 23;
                break;
              }
              return _context.abrupt("return", tentativeSelector);
            case 23:
              unintendedMatch = nextMatch.value;
              seekerAtTarget.seekToChunk(target.startChunk, target.startIndex - prefix.length);
              seekerAtUnintendedMatch.seekToChunk(unintendedMatch.startChunk, unintendedMatch.startIndex - prefix.length);
              extraPrefix = readUntilDifferent(seekerAtTarget, seekerAtUnintendedMatch, true);
              if (extraPrefix !== void 0 && !minimalContext) extraPrefix = readUntilWhitespace(seekerAtTarget, maxWordLength, true) + extraPrefix;
              seekerAtTarget.seekToChunk(target.endChunk, target.endIndex + suffix.length);
              seekerAtUnintendedMatch.seekToChunk(unintendedMatch.endChunk, unintendedMatch.endIndex + suffix.length);
              extraSuffix = readUntilDifferent(seekerAtTarget, seekerAtUnintendedMatch, false);
              if (extraSuffix !== void 0 && !minimalContext) extraSuffix = extraSuffix + readUntilWhitespace(seekerAtTarget, maxWordLength, false);
              if (!minimalContext) {
                _context.next = 44;
                break;
              }
              if (!(extraPrefix !== void 0 && (extraSuffix === void 0 || extraPrefix.length <= extraSuffix.length))) {
                _context.next = 37;
                break;
              }
              prefix = extraPrefix + prefix;
              _context.next = 42;
              break;
            case 37:
              if (!(extraSuffix !== void 0)) {
                _context.next = 41;
                break;
              }
              suffix = suffix + extraSuffix;
              _context.next = 42;
              break;
            case 41:
              throw new Error("Target cannot be disambiguated; how could that have happened\u203D");
            case 42:
              _context.next = 46;
              break;
            case 44:
              if (extraPrefix !== void 0) prefix = extraPrefix + prefix;
              if (extraSuffix !== void 0) suffix = suffix + extraSuffix;
            case 46:
              _context.next = 11;
              break;
            case 48:
            case "end":
              return _context.stop();
          }
        }
      }, _callee);
    }));
    return _describeTextQuote.apply(this, arguments);
  }
  function readUntilDifferent(seeker1, seeker2, reverse) {
    var result = "";
    while (true) {
      var nextCharacter = void 0;
      try {
        nextCharacter = seeker1.read(reverse ? -1 : 1);
      } catch (err) {
        return void 0;
      }
      result = reverse ? nextCharacter + result : result + nextCharacter;
      var comparisonCharacter = void 0;
      try {
        comparisonCharacter = seeker2.read(reverse ? -1 : 1);
      } catch (err) {
        if (!(err instanceof RangeError)) throw err;
      }
      if (nextCharacter !== comparisonCharacter) return result;
    }
  }
  function readUntilWhitespace(seeker) {
    var limit = arguments.length > 1 && arguments[1] !== void 0 ? arguments[1] : Infinity;
    var reverse = arguments.length > 2 && arguments[2] !== void 0 ? arguments[2] : false;
    var result = "";
    while (result.length < limit) {
      var nextCharacter = void 0;
      try {
        nextCharacter = seeker.read(reverse ? -1 : 1);
      } catch (err) {
        if (!(err instanceof RangeError)) throw err;
        break;
      }
      if (isWhitespace(nextCharacter)) {
        seeker.seekBy(reverse ? 1 : -1);
        break;
      }
      result = reverse ? nextCharacter + result : result + nextCharacter;
    }
    return result;
  }
  function isWhitespace(s) {
    return /^\s+$/.test(s);
  }

  // node_modules/@apache-annotator/selector/lib/text/match-text-quote.js
  var import_slice4 = __toESM(require_slice7(), 1);
  var import_from3 = __toESM(require_from6(), 1);
  var import_symbol6 = __toESM(require_symbol6(), 1);
  var import_get_iterator_method3 = __toESM(require_get_iterator_method7(), 1);
  var import_get_iterator2 = __toESM(require_get_iterator7(), 1);
  var import_regenerator5 = __toESM(require_regenerator2(), 1);
  var import_starts_with = __toESM(require_starts_with7(), 1);
  var import_filter = __toESM(require_filter7(), 1);
  function _createForOfIteratorHelper(o, allowArrayLike) {
    var it;
    if (typeof import_symbol6.default === "undefined" || (0, import_get_iterator_method3.default)(o) == null) {
      if (Array.isArray(o) || (it = _unsupportedIterableToArray2(o)) || allowArrayLike && o && typeof o.length === "number") {
        if (it) o = it;
        var i = 0;
        var F = function F2() {
        };
        return { s: F, n: function n() {
          if (i >= o.length) return { done: true };
          return { done: false, value: o[i++] };
        }, e: function e(_e) {
          throw _e;
        }, f: F };
      }
      throw new TypeError("Invalid attempt to iterate non-iterable instance.\nIn order to be iterable, non-array objects must have a [Symbol.iterator]() method.");
    }
    var normalCompletion = true, didErr = false, err;
    return { s: function s() {
      it = (0, import_get_iterator2.default)(o);
    }, n: function n() {
      var step = it.next();
      normalCompletion = step.done;
      return step;
    }, e: function e(_e2) {
      didErr = true;
      err = _e2;
    }, f: function f() {
      try {
        if (!normalCompletion && it.return != null) it.return();
      } finally {
        if (didErr) throw err;
      }
    } };
  }
  function _unsupportedIterableToArray2(o, minLen) {
    var _context2;
    if (!o) return;
    if (typeof o === "string") return _arrayLikeToArray2(o, minLen);
    var n = (0, import_slice4.default)(_context2 = Object.prototype.toString.call(o)).call(_context2, 8, -1);
    if (n === "Object" && o.constructor) n = o.constructor.name;
    if (n === "Map" || n === "Set") return (0, import_from3.default)(o);
    if (n === "Arguments" || /^(?:Ui|I)nt(?:8|16|32)(?:Clamped)?Array$/.test(n)) return _arrayLikeToArray2(o, minLen);
  }
  function _arrayLikeToArray2(arr, len) {
    if (len == null || len > arr.length) len = arr.length;
    for (var i = 0, arr2 = new Array(len); i < len; i++) {
      arr2[i] = arr[i];
    }
    return arr2;
  }
  function textQuoteSelectorMatcher(selector) {
    return /* @__PURE__ */ (function() {
      var _matchAll = _wrapAsyncGenerator(/* @__PURE__ */ import_regenerator5.default.mark(function _callee(textChunks) {
        var exact, prefix, suffix, searchPattern, partialMatches, isFirstChunk, chunk, chunkValue, remainingPartialMatches, _iterator, _step, partialMatch, charactersMatched, charactersUntilMatchEnd, charactersUntilMatchStart, charactersUntilSuffixEnd, fromIndex, patternStartIndex, newPartialMatches, searchStartPoint, _loop, i, _iterator2, _step2, partialMatchStartIndex, _charactersMatched, _partialMatch;
        return import_regenerator5.default.wrap(function _callee$(_context) {
          while (1) {
            switch (_context.prev = _context.next) {
              case 0:
                exact = selector.exact;
                prefix = selector.prefix || "";
                suffix = selector.suffix || "";
                searchPattern = prefix + exact + suffix;
                partialMatches = [];
                isFirstChunk = true;
              case 6:
                chunk = textChunks.currentChunk;
                chunkValue = chunk.data;
                remainingPartialMatches = [];
                _iterator = _createForOfIteratorHelper(partialMatches);
                _context.prev = 10;
                _iterator.s();
              case 12:
                if ((_step = _iterator.n()).done) {
                  _context.next = 27;
                  break;
                }
                partialMatch = _step.value;
                charactersMatched = partialMatch.charactersMatched;
                if (partialMatch.endChunk === void 0) {
                  charactersUntilMatchEnd = prefix.length + exact.length - charactersMatched;
                  if (charactersUntilMatchEnd <= chunkValue.length) {
                    partialMatch.endChunk = chunk;
                    partialMatch.endIndex = charactersUntilMatchEnd;
                  }
                }
                if (partialMatch.startChunk === void 0) {
                  charactersUntilMatchStart = prefix.length - charactersMatched;
                  if (charactersUntilMatchStart < chunkValue.length || partialMatch.endChunk !== void 0) {
                    partialMatch.startChunk = chunk;
                    partialMatch.startIndex = charactersUntilMatchStart;
                  }
                }
                charactersUntilSuffixEnd = searchPattern.length - charactersMatched;
                if (!(charactersUntilSuffixEnd <= chunkValue.length)) {
                  _context.next = 24;
                  break;
                }
                if (!(0, import_starts_with.default)(chunkValue).call(chunkValue, searchPattern.substring(charactersMatched))) {
                  _context.next = 22;
                  break;
                }
                _context.next = 22;
                return partialMatch;
              case 22:
                _context.next = 25;
                break;
              case 24:
                if (chunkValue === searchPattern.substring(charactersMatched, charactersMatched + chunkValue.length)) {
                  partialMatch.charactersMatched += chunkValue.length;
                  remainingPartialMatches.push(partialMatch);
                }
              case 25:
                _context.next = 12;
                break;
              case 27:
                _context.next = 32;
                break;
              case 29:
                _context.prev = 29;
                _context.t0 = _context["catch"](10);
                _iterator.e(_context.t0);
              case 32:
                _context.prev = 32;
                _iterator.f();
                return _context.finish(32);
              case 35:
                partialMatches = remainingPartialMatches;
                if (!(searchPattern.length <= chunkValue.length)) {
                  _context.next = 49;
                  break;
                }
                fromIndex = 0;
              case 38:
                if (!(fromIndex <= chunkValue.length)) {
                  _context.next = 49;
                  break;
                }
                patternStartIndex = chunkValue.indexOf(searchPattern, fromIndex);
                if (!(patternStartIndex === -1)) {
                  _context.next = 42;
                  break;
                }
                return _context.abrupt("break", 49);
              case 42:
                fromIndex = patternStartIndex + 1;
                if (!(patternStartIndex === 0 && searchPattern.length === 0 && !isFirstChunk)) {
                  _context.next = 45;
                  break;
                }
                return _context.abrupt("continue", 38);
              case 45:
                _context.next = 47;
                return {
                  startChunk: chunk,
                  startIndex: patternStartIndex + prefix.length,
                  endChunk: chunk,
                  endIndex: patternStartIndex + prefix.length + exact.length
                };
              case 47:
                _context.next = 38;
                break;
              case 49:
                newPartialMatches = [];
                searchStartPoint = Math.max(chunkValue.length - searchPattern.length + 1, 0);
                _loop = function _loop2(i2) {
                  var character = chunkValue[i2];
                  newPartialMatches = (0, import_filter.default)(newPartialMatches).call(newPartialMatches, function(partialMatchStartIndex2) {
                    return character === searchPattern[i2 - partialMatchStartIndex2];
                  });
                  if (character === searchPattern[0]) newPartialMatches.push(i2);
                };
                for (i = searchStartPoint; i < chunkValue.length; i++) {
                  _loop(i);
                }
                _iterator2 = _createForOfIteratorHelper(newPartialMatches);
                try {
                  for (_iterator2.s(); !(_step2 = _iterator2.n()).done; ) {
                    partialMatchStartIndex = _step2.value;
                    _charactersMatched = chunkValue.length - partialMatchStartIndex;
                    _partialMatch = {
                      charactersMatched: _charactersMatched
                    };
                    if (_charactersMatched >= prefix.length + exact.length) {
                      _partialMatch.endChunk = chunk;
                      _partialMatch.endIndex = partialMatchStartIndex + prefix.length + exact.length;
                    }
                    if (_charactersMatched > prefix.length || _partialMatch.endChunk !== void 0) {
                      _partialMatch.startChunk = chunk;
                      _partialMatch.startIndex = partialMatchStartIndex + prefix.length;
                    }
                    partialMatches.push(_partialMatch);
                  }
                } catch (err) {
                  _iterator2.e(err);
                } finally {
                  _iterator2.f();
                }
                isFirstChunk = false;
              case 56:
                if (textChunks.nextChunk() !== null) {
                  _context.next = 6;
                  break;
                }
              case 57:
              case "end":
                return _context.stop();
            }
          }
        }, _callee, null, [[10, 29, 32, 35]]);
      }));
      function matchAll(_x) {
        return _matchAll.apply(this, arguments);
      }
      return matchAll;
    })();
  }

  // node_modules/@apache-annotator/selector/lib/text/describe-text-position.js
  var import_regenerator6 = __toESM(require_regenerator2(), 1);

  // node_modules/@apache-annotator/selector/lib/text/code-point-seeker.js
  var import_slice5 = __toESM(require_slice7(), 1);
  var import_concat2 = __toESM(require_concat7(), 1);

  // node_modules/@apache-annotator/selector/lib/text/match-text-position.js
  var import_regenerator7 = __toESM(require_regenerator2(), 1);

  // node_modules/@apache-annotator/dom/lib/text-node-chunker.js
  var import_construct4 = __toESM(require_construct6(), 1);

  // node_modules/@babel/runtime-corejs3/helpers/esm/inherits.js
  var import_create = __toESM(require_create5(), 1);
  var import_define_property3 = __toESM(require_define_property5(), 1);

  // node_modules/@babel/runtime-corejs3/helpers/esm/setPrototypeOf.js
  var import_set_prototype_of = __toESM(require_set_prototype_of5(), 1);
  var import_bind = __toESM(require_bind6(), 1);
  function _setPrototypeOf(t, e) {
    var _context;
    return _setPrototypeOf = import_set_prototype_of.default ? (0, import_bind.default)(_context = import_set_prototype_of.default).call(_context) : function(t2, e2) {
      return t2.__proto__ = e2, t2;
    }, _setPrototypeOf(t, e);
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/inherits.js
  function _inherits(t, e) {
    if ("function" != typeof e && null !== e) throw new TypeError("Super expression must either be null or a function");
    t.prototype = (0, import_create.default)(e && e.prototype, {
      constructor: {
        value: t,
        writable: true,
        configurable: true
      }
    }), (0, import_define_property3.default)(t, "prototype", {
      writable: false
    }), e && _setPrototypeOf(t, e);
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/assertThisInitialized.js
  function _assertThisInitialized(e) {
    if (void 0 === e) throw new ReferenceError("this hasn't been initialised - super() hasn't been called");
    return e;
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/possibleConstructorReturn.js
  function _possibleConstructorReturn(t, e) {
    if (e && ("object" == _typeof(e) || "function" == typeof e)) return e;
    if (void 0 !== e) throw new TypeError("Derived constructors may only return object or undefined");
    return _assertThisInitialized(t);
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/getPrototypeOf.js
  var import_set_prototype_of2 = __toESM(require_set_prototype_of5(), 1);
  var import_bind2 = __toESM(require_bind6(), 1);
  var import_get_prototype_of = __toESM(require_get_prototype_of5(), 1);
  function _getPrototypeOf(t) {
    var _context;
    return _getPrototypeOf = import_set_prototype_of2.default ? (0, import_bind2.default)(_context = import_get_prototype_of.default).call(_context) : function(t2) {
      return t2.__proto__ || (0, import_get_prototype_of.default)(t2);
    }, _getPrototypeOf(t);
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/wrapNativeSuper.js
  var import_map2 = __toESM(require_map12(), 1);
  var import_create2 = __toESM(require_create5(), 1);

  // node_modules/@babel/runtime-corejs3/helpers/esm/isNativeFunction.js
  var import_index_of = __toESM(require_index_of6(), 1);
  function _isNativeFunction(t) {
    try {
      var _context;
      return -1 !== (0, import_index_of.default)(_context = Function.toString.call(t)).call(_context, "[native code]");
    } catch (n) {
      return "function" == typeof t;
    }
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/construct.js
  var import_construct2 = __toESM(require_construct5(), 1);
  var import_push2 = __toESM(require_push6(), 1);
  var import_bind3 = __toESM(require_bind6(), 1);

  // node_modules/@babel/runtime-corejs3/helpers/esm/isNativeReflectConstruct.js
  var import_construct = __toESM(require_construct5(), 1);
  function _isNativeReflectConstruct() {
    try {
      var t = !Boolean.prototype.valueOf.call((0, import_construct.default)(Boolean, [], function() {
      }));
    } catch (t2) {
    }
    return (_isNativeReflectConstruct = function _isNativeReflectConstruct3() {
      return !!t;
    })();
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/construct.js
  function _construct(t, e, r) {
    if (_isNativeReflectConstruct()) return import_construct2.default.apply(null, arguments);
    var o = [null];
    (0, import_push2.default)(o).apply(o, e);
    var p = new ((0, import_bind3.default)(t).apply(t, o))();
    return r && _setPrototypeOf(p, r.prototype), p;
  }

  // node_modules/@babel/runtime-corejs3/helpers/esm/wrapNativeSuper.js
  function _wrapNativeSuper(t) {
    var r = "function" == typeof import_map2.default ? new import_map2.default() : void 0;
    return _wrapNativeSuper = function _wrapNativeSuper2(t2) {
      if (null === t2 || !_isNativeFunction(t2)) return t2;
      if ("function" != typeof t2) throw new TypeError("Super expression must either be null or a function");
      if (void 0 !== r) {
        if (r.has(t2)) return r.get(t2);
        r.set(t2, Wrapper);
      }
      function Wrapper() {
        return _construct(t2, arguments, _getPrototypeOf(this).constructor);
      }
      return Wrapper.prototype = (0, import_create2.default)(t2.prototype, {
        constructor: {
          value: Wrapper,
          enumerable: false,
          writable: true,
          configurable: true
        }
      }), _setPrototypeOf(Wrapper, t2);
    }, _wrapNativeSuper(t);
  }

  // node_modules/@apache-annotator/dom/lib/normalize-range.js
  function normalizeRange(range, scope) {
    var document2 = ownerDocument(range);
    var walker = document2.createTreeWalker(document2, NodeFilter.SHOW_TEXT, {
      acceptNode: function acceptNode(node) {
        return !scope || scope.intersectsNode(node) ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT;
      }
    });
    var _snapBoundaryPointToT = snapBoundaryPointToTextNode(range.startContainer, range.startOffset), _snapBoundaryPointToT2 = _slicedToArray(_snapBoundaryPointToT, 2), startContainer = _snapBoundaryPointToT2[0], startOffset = _snapBoundaryPointToT2[1];
    walker.currentNode = startContainer;
    while (startOffset === startContainer.length && walker.nextNode()) {
      startContainer = walker.currentNode;
      startOffset = 0;
    }
    range.setStart(startContainer, startOffset);
    var _snapBoundaryPointToT3 = snapBoundaryPointToTextNode(range.endContainer, range.endOffset), _snapBoundaryPointToT4 = _slicedToArray(_snapBoundaryPointToT3, 2), endContainer = _snapBoundaryPointToT4[0], endOffset = _snapBoundaryPointToT4[1];
    walker.currentNode = endContainer;
    while (endOffset === 0 && walker.previousNode()) {
      endContainer = walker.currentNode;
      endOffset = endContainer.length;
    }
    range.setEnd(endContainer, endOffset);
    return range;
  }
  function snapBoundaryPointToTextNode(node, offset) {
    var _node$ownerDocument;
    if (isText(node)) return [node, offset];
    var curNode;
    if (isCharacterData(node)) {
      curNode = node;
    } else if (offset < node.childNodes.length) {
      curNode = node.childNodes[offset];
    } else {
      curNode = node;
      while (curNode.nextSibling === null) {
        if (curNode.parentNode === null)
          throw new Error("not implemented");
        curNode = curNode.parentNode;
      }
      curNode = curNode.nextSibling;
    }
    if (isText(curNode)) return [curNode, 0];
    var document2 = (_node$ownerDocument = node.ownerDocument) !== null && _node$ownerDocument !== void 0 ? _node$ownerDocument : node;
    var walker = document2.createTreeWalker(document2, NodeFilter.SHOW_TEXT);
    walker.currentNode = curNode;
    if (walker.nextNode() !== null) {
      return [walker.currentNode, 0];
    } else if (walker.previousNode() !== null) {
      return [walker.currentNode, walker.currentNode.length];
    } else {
      throw new Error("Document contains no text nodes.");
    }
  }
  function isText(node) {
    return node.nodeType === Node.TEXT_NODE;
  }
  function isCharacterData(node) {
    return node.nodeType === Node.PROCESSING_INSTRUCTION_NODE || node.nodeType === Node.COMMENT_NODE || node.nodeType === Node.TEXT_NODE;
  }

  // node_modules/@apache-annotator/dom/lib/text-node-chunker.js
  function _createSuper(Derived) {
    var hasNativeReflectConstruct = _isNativeReflectConstruct2();
    return function _createSuperInternal() {
      var Super = _getPrototypeOf(Derived), result;
      if (hasNativeReflectConstruct) {
        var NewTarget = _getPrototypeOf(this).constructor;
        result = (0, import_construct4.default)(Super, arguments, NewTarget);
      } else {
        result = Super.apply(this, arguments);
      }
      return _possibleConstructorReturn(this, result);
    };
  }
  function _isNativeReflectConstruct2() {
    if (typeof Reflect === "undefined" || !import_construct4.default) return false;
    if (import_construct4.default.sham) return false;
    if (typeof Proxy === "function") return true;
    try {
      Boolean.prototype.valueOf.call((0, import_construct4.default)(Boolean, [], function() {
      }));
      return true;
    } catch (e) {
      return false;
    }
  }
  var EmptyScopeError = /* @__PURE__ */ (function(_TypeError) {
    _inherits(EmptyScopeError2, _TypeError);
    var _super = _createSuper(EmptyScopeError2);
    function EmptyScopeError2(message) {
      _classCallCheck(this, EmptyScopeError2);
      return _super.call(this, message || "Scope contains no text nodes.");
    }
    return EmptyScopeError2;
  })(/* @__PURE__ */ _wrapNativeSuper(TypeError));
  var OutOfScopeError = /* @__PURE__ */ (function(_TypeError2) {
    _inherits(OutOfScopeError2, _TypeError2);
    var _super2 = _createSuper(OutOfScopeError2);
    function OutOfScopeError2(message) {
      _classCallCheck(this, OutOfScopeError2);
      return _super2.call(this, message || "Cannot convert node to chunk, as it falls outside of chunker\u2019s scope.");
    }
    return OutOfScopeError2;
  })(/* @__PURE__ */ _wrapNativeSuper(TypeError));
  var TextNodeChunker = /* @__PURE__ */ (function() {
    function TextNodeChunker2(scope) {
      var _this = this;
      _classCallCheck(this, TextNodeChunker2);
      _defineProperty(this, "scope", void 0);
      _defineProperty(this, "iter", void 0);
      this.scope = toRange(scope);
      this.iter = ownerDocument(scope).createNodeIterator(this.scope.commonAncestorContainer, NodeFilter.SHOW_TEXT, {
        acceptNode: function acceptNode(node) {
          return _this.scope.intersectsNode(node) ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT;
        }
      });
      this.iter.nextNode();
      if (!isText2(this.iter.referenceNode)) {
        var nextNode = this.iter.nextNode();
        if (nextNode === null) throw new EmptyScopeError();
      }
    }
    _createClass(TextNodeChunker2, [{
      key: "currentChunk",
      get: function get() {
        var node = this.iter.referenceNode;
        if (!isText2(node)) throw new EmptyScopeError();
        return this.nodeToChunk(node);
      }
    }, {
      key: "nodeToChunk",
      value: function nodeToChunk(node) {
        if (!this.scope.intersectsNode(node)) throw new OutOfScopeError();
        var startOffset = node === this.scope.startContainer ? this.scope.startOffset : 0;
        var endOffset = node === this.scope.endContainer ? this.scope.endOffset : node.length;
        return {
          node,
          startOffset,
          endOffset,
          data: node.data.substring(startOffset, endOffset),
          equals: function equals(other) {
            return other.node === this.node && other.startOffset === this.startOffset && other.endOffset === this.endOffset;
          }
        };
      }
    }, {
      key: "rangeToChunkRange",
      value: function rangeToChunkRange(range) {
        range = range.cloneRange();
        if (range.compareBoundaryPoints(Range.START_TO_START, this.scope) === -1) range.setStart(this.scope.startContainer, this.scope.startOffset);
        if (range.compareBoundaryPoints(Range.END_TO_END, this.scope) === 1) range.setEnd(this.scope.endContainer, this.scope.endOffset);
        var textRange = normalizeRange(range, this.scope);
        var startChunk = this.nodeToChunk(textRange.startContainer);
        var startIndex = textRange.startOffset - startChunk.startOffset;
        var endChunk = this.nodeToChunk(textRange.endContainer);
        var endIndex = textRange.endOffset - endChunk.startOffset;
        return {
          startChunk,
          startIndex,
          endChunk,
          endIndex
        };
      }
    }, {
      key: "chunkRangeToRange",
      value: function chunkRangeToRange(chunkRange) {
        var range = ownerDocument(this.scope).createRange();
        range.setStart(chunkRange.startChunk.node, chunkRange.startIndex + chunkRange.startChunk.startOffset);
        range.setEnd(chunkRange.endChunk.node, chunkRange.endIndex + chunkRange.endChunk.startOffset);
        return range;
      }
    }, {
      key: "nextChunk",
      value: function nextChunk() {
        if (this.iter.pointerBeforeReferenceNode) this.iter.nextNode();
        if (this.iter.nextNode()) return this.currentChunk;
        else return null;
      }
    }, {
      key: "previousChunk",
      value: function previousChunk() {
        if (!this.iter.pointerBeforeReferenceNode) this.iter.previousNode();
        if (this.iter.previousNode()) return this.currentChunk;
        else return null;
      }
    }, {
      key: "precedesCurrentChunk",
      value: function precedesCurrentChunk(chunk) {
        if (this.currentChunk === null) return false;
        return !!(this.currentChunk.node.compareDocumentPosition(chunk.node) & Node.DOCUMENT_POSITION_PRECEDING);
      }
    }]);
    return TextNodeChunker2;
  })();
  function isText2(node) {
    return node.nodeType === Node.TEXT_NODE;
  }

  // node_modules/@apache-annotator/dom/lib/text-quote/describe.js
  function describeTextQuote2(_x, _x2) {
    return _describeTextQuote2.apply(this, arguments);
  }
  function _describeTextQuote2() {
    _describeTextQuote2 = _asyncToGenerator(/* @__PURE__ */ import_regenerator9.default.mark(function _callee(range, scope) {
      var options, scopeAsRange, chunker, _args = arguments;
      return import_regenerator9.default.wrap(function _callee$(_context) {
        while (1) {
          switch (_context.prev = _context.next) {
            case 0:
              options = _args.length > 2 && _args[2] !== void 0 ? _args[2] : {};
              scopeAsRange = toRange(scope !== null && scope !== void 0 ? scope : ownerDocument(range));
              chunker = new TextNodeChunker(scopeAsRange);
              _context.next = 5;
              return describeTextQuote(chunker.rangeToChunkRange(range), function() {
                return new TextNodeChunker(scopeAsRange);
              }, options);
            case 5:
              return _context.abrupt("return", _context.sent);
            case 6:
            case "end":
              return _context.stop();
          }
        }
      }, _callee);
    }));
    return _describeTextQuote2.apply(this, arguments);
  }

  // node_modules/@apache-annotator/dom/lib/text-quote/match.js
  var import_regenerator10 = __toESM(require_regenerator2(), 1);
  function createTextQuoteSelectorMatcher(selector) {
    var abstractMatcher = textQuoteSelectorMatcher(selector);
    return /* @__PURE__ */ (function() {
      var _matchAll = _wrapAsyncGenerator(/* @__PURE__ */ import_regenerator10.default.mark(function _callee(scope) {
        var textChunks, _iteratorNormalCompletion, _didIteratorError, _iteratorError, _iterator, _step, _value, abstractMatch;
        return import_regenerator10.default.wrap(function _callee$(_context) {
          while (1) {
            switch (_context.prev = _context.next) {
              case 0:
                _context.prev = 0;
                textChunks = new TextNodeChunker(scope);
                _context.next = 11;
                break;
              case 4:
                _context.prev = 4;
                _context.t0 = _context["catch"](0);
                if (!(_context.t0 instanceof EmptyScopeError)) {
                  _context.next = 10;
                  break;
                }
                return _context.abrupt("return");
              case 10:
                throw _context.t0;
              case 11:
                _iteratorNormalCompletion = true;
                _didIteratorError = false;
                _context.prev = 13;
                _iterator = _asyncIterator(abstractMatcher(textChunks));
              case 15:
                _context.next = 17;
                return _awaitAsyncGenerator(_iterator.next());
              case 17:
                _step = _context.sent;
                _iteratorNormalCompletion = _step.done;
                _context.next = 21;
                return _awaitAsyncGenerator(_step.value);
              case 21:
                _value = _context.sent;
                if (_iteratorNormalCompletion) {
                  _context.next = 29;
                  break;
                }
                abstractMatch = _value;
                _context.next = 26;
                return textChunks.chunkRangeToRange(abstractMatch);
              case 26:
                _iteratorNormalCompletion = true;
                _context.next = 15;
                break;
              case 29:
                _context.next = 35;
                break;
              case 31:
                _context.prev = 31;
                _context.t1 = _context["catch"](13);
                _didIteratorError = true;
                _iteratorError = _context.t1;
              case 35:
                _context.prev = 35;
                _context.prev = 36;
                if (!(!_iteratorNormalCompletion && _iterator.return != null)) {
                  _context.next = 40;
                  break;
                }
                _context.next = 40;
                return _awaitAsyncGenerator(_iterator.return());
              case 40:
                _context.prev = 40;
                if (!_didIteratorError) {
                  _context.next = 43;
                  break;
                }
                throw _iteratorError;
              case 43:
                return _context.finish(40);
              case 44:
                return _context.finish(35);
              case 45:
              case "end":
                return _context.stop();
            }
          }
        }, _callee, null, [[0, 4], [13, 31, 35, 45], [36, , 40, 44]]);
      }));
      function matchAll(_x) {
        return _matchAll.apply(this, arguments);
      }
      return matchAll;
    })();
  }

  // node_modules/@apache-annotator/dom/lib/text-position/describe.js
  var import_regenerator11 = __toESM(require_regenerator2(), 1);

  // node_modules/@apache-annotator/dom/lib/text-position/match.js
  var import_regenerator12 = __toESM(require_regenerator2(), 1);

  // node_modules/@apache-annotator/dom/lib/highlight-text.js
  var import_keys = __toESM(require_keys6(), 1);
  var import_slice6 = __toESM(require_slice7(), 1);
  var import_from4 = __toESM(require_from6(), 1);
  var import_symbol7 = __toESM(require_symbol6(), 1);
  var import_get_iterator_method4 = __toESM(require_get_iterator_method7(), 1);
  var import_get_iterator3 = __toESM(require_get_iterator7(), 1);
  function _createForOfIteratorHelper2(o, allowArrayLike) {
    var it;
    if (typeof import_symbol7.default === "undefined" || (0, import_get_iterator_method4.default)(o) == null) {
      if (Array.isArray(o) || (it = _unsupportedIterableToArray3(o)) || allowArrayLike && o && typeof o.length === "number") {
        if (it) o = it;
        var i = 0;
        var F = function F2() {
        };
        return { s: F, n: function n() {
          if (i >= o.length) return { done: true };
          return { done: false, value: o[i++] };
        }, e: function e(_e) {
          throw _e;
        }, f: F };
      }
      throw new TypeError("Invalid attempt to iterate non-iterable instance.\nIn order to be iterable, non-array objects must have a [Symbol.iterator]() method.");
    }
    var normalCompletion = true, didErr = false, err;
    return { s: function s() {
      it = (0, import_get_iterator3.default)(o);
    }, n: function n() {
      var step = it.next();
      normalCompletion = step.done;
      return step;
    }, e: function e(_e2) {
      didErr = true;
      err = _e2;
    }, f: function f() {
      try {
        if (!normalCompletion && it.return != null) it.return();
      } finally {
        if (didErr) throw err;
      }
    } };
  }
  function _unsupportedIterableToArray3(o, minLen) {
    var _context;
    if (!o) return;
    if (typeof o === "string") return _arrayLikeToArray3(o, minLen);
    var n = (0, import_slice6.default)(_context = Object.prototype.toString.call(o)).call(_context, 8, -1);
    if (n === "Object" && o.constructor) n = o.constructor.name;
    if (n === "Map" || n === "Set") return (0, import_from4.default)(o);
    if (n === "Arguments" || /^(?:Ui|I)nt(?:8|16|32)(?:Clamped)?Array$/.test(n)) return _arrayLikeToArray3(o, minLen);
  }
  function _arrayLikeToArray3(arr, len) {
    if (len == null || len > arr.length) len = arr.length;
    for (var i = 0, arr2 = new Array(len); i < len; i++) {
      arr2[i] = arr[i];
    }
    return arr2;
  }
  function highlightText(target) {
    var tagName = arguments.length > 1 && arguments[1] !== void 0 ? arguments[1] : "mark";
    var attributes = arguments.length > 2 && arguments[2] !== void 0 ? arguments[2] : {};
    var nodes = textNodesInRange(toRange(target));
    var highlightElements = [];
    var _iterator = _createForOfIteratorHelper2(nodes), _step;
    try {
      for (_iterator.s(); !(_step = _iterator.n()).done; ) {
        var node = _step.value;
        var highlightElement = wrapNodeInHighlight(node, tagName, attributes);
        highlightElements.push(highlightElement);
      }
    } catch (err) {
      _iterator.e(err);
    } finally {
      _iterator.f();
    }
    function removeHighlights() {
      var _iterator2 = _createForOfIteratorHelper2(highlightElements), _step2;
      try {
        for (_iterator2.s(); !(_step2 = _iterator2.n()).done; ) {
          var highlightElement2 = _step2.value;
          removeHighlight(highlightElement2);
        }
      } catch (err) {
        _iterator2.e(err);
      } finally {
        _iterator2.f();
      }
    }
    return removeHighlights;
  }
  function textNodesInRange(range) {
    if (isTextNode(range.startContainer) && range.startOffset > 0) {
      var endOffset = range.endOffset;
      var createdNode = range.startContainer.splitText(range.startOffset);
      if (range.endContainer === range.startContainer) {
        range.setEnd(createdNode, endOffset - range.startOffset);
      }
      range.setStart(createdNode, 0);
    }
    if (isTextNode(range.endContainer) && range.endOffset < range.endContainer.length) {
      range.endContainer.splitText(range.endOffset);
    }
    var walker = ownerDocument(range).createTreeWalker(range.commonAncestorContainer, NodeFilter.SHOW_TEXT, {
      acceptNode: function acceptNode(node) {
        return range.intersectsNode(node) ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT;
      }
    });
    walker.currentNode = range.startContainer;
    var nodes = [];
    if (isTextNode(walker.currentNode)) nodes.push(walker.currentNode);
    while (walker.nextNode() && range.comparePoint(walker.currentNode, 0) !== 1) {
      nodes.push(walker.currentNode);
    }
    return nodes;
  }
  function wrapNodeInHighlight(node, tagName, attributes) {
    var document2 = node.ownerDocument;
    var highlightElement = document2.createElement(tagName);
    (0, import_keys.default)(attributes).forEach(function(key) {
      highlightElement.setAttribute(key, attributes[key]);
    });
    var tempRange = document2.createRange();
    tempRange.selectNode(node);
    tempRange.surroundContents(highlightElement);
    return highlightElement;
  }
  function removeHighlight(highlightElement) {
    if (!highlightElement.parentNode) return;
    if (highlightElement.childNodes.length === 1) {
      highlightElement.replaceWith(highlightElement.firstChild);
    } else {
      while (highlightElement.firstChild) {
        highlightElement.parentNode.insertBefore(highlightElement.firstChild, highlightElement);
      }
      highlightElement.remove();
    }
  }
  function isTextNode(node) {
    return node.nodeType === Node.TEXT_NODE;
  }
  return __toCommonJS(entry_exports);
})();
/*! Bundled license information:

@babel/runtime-corejs3/helpers/regenerator.js:
  (*! regenerator-runtime -- Copyright (c) 2014-present, Facebook, Inc. -- license (MIT): https://github.com/babel/babel/blob/main/packages/babel-helpers/LICENSE *)

@apache-annotator/dom/lib/owner-document.js:
@apache-annotator/dom/lib/to-range.js:
@apache-annotator/dom/lib/css.js:
@apache-annotator/dom/lib/range/cartesian.js:
@apache-annotator/dom/lib/range/match.js:
@apache-annotator/dom/lib/range/index.js:
@apache-annotator/selector/lib/text/chunker.js:
@apache-annotator/selector/lib/text/seeker.js:
@apache-annotator/selector/lib/text/describe-text-quote.js:
@apache-annotator/selector/lib/text/match-text-quote.js:
@apache-annotator/selector/lib/text/code-point-seeker.js:
@apache-annotator/selector/lib/text/describe-text-position.js:
@apache-annotator/selector/lib/text/match-text-position.js:
@apache-annotator/selector/lib/text/index.js:
@apache-annotator/selector/lib/index.js:
@apache-annotator/dom/lib/normalize-range.js:
@apache-annotator/dom/lib/text-node-chunker.js:
@apache-annotator/dom/lib/text-quote/describe.js:
@apache-annotator/dom/lib/text-quote/match.js:
@apache-annotator/dom/lib/text-quote/index.js:
@apache-annotator/dom/lib/text-position/describe.js:
@apache-annotator/dom/lib/text-position/match.js:
@apache-annotator/dom/lib/text-position/index.js:
@apache-annotator/dom/lib/highlight-text.js:
@apache-annotator/dom/lib/index.js:
  (**
   * SPDX-FileCopyrightText: 2016-2021 The Apache Software Foundation
   * SPDX-License-Identifier: Apache-2.0
   * @license
   * Licensed to the Apache Software Foundation (ASF) under one
   * or more contributor license agreements.  See the NOTICE file
   * distributed with this work for additional information
   * regarding copyright ownership.  The ASF licenses this file
   * to you under the Apache License, Version 2.0 (the
   * "License"); you may not use this file except in compliance
   * with the License.  You may obtain a copy of the License at
   *
   *   http://www.apache.org/licenses/LICENSE-2.0
   *
   * Unless required by applicable law or agreed to in writing,
   * software distributed under the License is distributed on an
   * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
   * KIND, either express or implied.  See the License for the
   * specific language governing permissions and limitations
   * under the License.
   *)
*/